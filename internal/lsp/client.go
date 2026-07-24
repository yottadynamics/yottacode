package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultStartupTimeout = 3 * time.Second
	defaultRequestTimeout = 5 * time.Second
	maxMessageBytes       = 4 * 1024 * 1024
)

// ErrUnsupportedCapability marks an LSP method that the initialized server did
// not advertise. Tool wrappers turn this into an actionable unavailable result
// instead of showing a misleading empty response.
var ErrUnsupportedCapability = errors.New("unsupported LSP capability")

// Position is a zero-based LSP text position.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Location is a compact source location returned to the agent tools.
type Location struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// Symbol is a workspace symbol result from an LSP server.
type Symbol struct {
	Name      string
	Kind      string
	Container string
	Location  Location
}

// Diagnostic is a compile/type/lint message published by an LSP server.
type Diagnostic struct {
	Path      string
	Line      int
	Character int
	Severity  string
	Source    string
	Message   string
}

// CodeAction is a read-only description of a server-offered fix/refactor.
type CodeAction struct {
	Title string
	Kind  string
}

// CallHierarchyItem is one incoming/outgoing call hierarchy row.
type CallHierarchyItem struct {
	Name      string
	Kind      string
	Detail    string
	Location  Location
	Direction string
}

type serverCapabilities struct {
	WorkspaceSymbol bool
	Definition      bool
	References      bool
	Hover           bool
	CodeAction      bool
	CallHierarchy   bool
}

// Client owns one short-lived language-server process. yottacode starts a
// process per tool call instead of keeping editor-style background servers so
// lifecycle, permissions, and session teardown stay simple.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  bytes.Buffer
	rootURI string
	caps    serverCapabilities
	capOK   bool

	mu     sync.Mutex
	nextID int64
}

// NewClient starts and initializes the server for lang at root.
func NewClient(ctx context.Context, lang Language, root string) (*Client, error) {
	if len(lang.Command) == 0 {
		return nil, fmt.Errorf("%s language server command is not configured", lang.Name)
	}
	if _, err := exec.LookPath(lang.Command[0]); err != nil {
		return nil, fmt.Errorf("%s language server %q not found on PATH", lang.Name, lang.Command[0])
	}
	startCtx, cancel := context.WithTimeout(ctx, defaultStartupTimeout)
	defer cancel()
	cmd := exec.Command(lang.Command[0], lang.Command[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &cappedWriter{buf: &stderr, max: 64 * 1024}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", lang.Command[0], err)
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		stderr:  stderr,
		rootURI: pathToURI(root),
	}
	if err := c.initialize(startCtx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   c.rootURI,
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"symbol": map[string]any{},
			},
			"textDocument": map[string]any{
				"definition":         map[string]any{},
				"references":         map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"codeAction":         map[string]any{},
				"publishDiagnostics": map[string]any{},
				"callHierarchy":      map[string]any{},
			},
		},
	}
	raw, err := c.requestLocked(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w%s", err, c.stderrSuffix())
	}
	c.caps, c.capOK = parseServerCapabilities(raw), true
	return c.notifyLocked("initialized", map[string]any{})
}

// Close asks the server to shut down, then tears down the process if it is
// still running. Errors are intentionally swallowed by callers via defer; the
// tool result has already been produced by this point.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, _ = c.requestLocked(ctx, "shutdown", nil)
		_ = c.notifyLocked("exit", nil)
	}()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

// WorkspaceSymbols runs workspace/symbol and returns normalized results.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]Symbol, error) {
	if err := c.requireCapability(c.caps.WorkspaceSymbol, "workspace/symbol"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	raw, err := c.request(ctx, "workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	var items []struct {
		Name          string `json:"name"`
		Kind          int    `json:"kind"`
		ContainerName string `json:"containerName"`
		Location      struct {
			URI   string `json:"uri"`
			Range struct {
				Start Position `json:"start"`
			} `json:"range"`
		} `json:"location"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse workspace/symbol response: %w", err)
	}
	out := make([]Symbol, 0, len(items))
	for _, item := range items {
		out = append(out, Symbol{
			Name:      item.Name,
			Kind:      symbolKindName(item.Kind),
			Container: item.ContainerName,
			Location:  Location{Path: uriToPath(item.Location.URI), Line: item.Location.Range.Start.Line, Character: item.Location.Range.Start.Character},
		})
	}
	return out, nil
}

// Definition runs textDocument/definition at pos.
func (c *Client) Definition(ctx context.Context, path string, pos Position) ([]Location, error) {
	if err := c.requireCapability(c.caps.Definition, "textDocument/definition"); err != nil {
		return nil, err
	}
	if err := c.openDocument(ctx, path); err != nil {
		return nil, err
	}
	return c.locationRequest(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     pos,
	})
}

// References runs textDocument/references at pos.
func (c *Client) References(ctx context.Context, path string, pos Position, includeDeclaration bool) ([]Location, error) {
	if err := c.requireCapability(c.caps.References, "textDocument/references"); err != nil {
		return nil, err
	}
	if err := c.openDocument(ctx, path); err != nil {
		return nil, err
	}
	return c.locationRequest(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": includeDeclaration},
	})
}

// Hover returns type/doc information at a source position.
func (c *Client) Hover(ctx context.Context, path string, pos Position) (string, error) {
	if err := c.requireCapability(c.caps.Hover, "textDocument/hover"); err != nil {
		return "", err
	}
	if err := c.openDocument(ctx, path); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	raw, err := c.request(ctx, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": pathToURI(path)}, "position": pos})
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return "", err
	}
	return hoverText(raw)
}

// CodeActions lists server-offered actions for a range without applying them.
func (c *Client) CodeActions(ctx context.Context, path string, start, end Position) ([]CodeAction, error) {
	if err := c.requireCapability(c.caps.CodeAction, "textDocument/codeAction"); err != nil {
		return nil, err
	}
	if err := c.openDocument(ctx, path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	raw, err := c.request(ctx, "textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"range":        map[string]any{"start": start, "end": end},
		"context":      map[string]any{"diagnostics": []any{}},
	})
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil, err
	}
	var items []struct{ Title, Kind string }
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse codeAction response: %w", err)
	}
	out := make([]CodeAction, 0, len(items))
	for _, item := range items {
		out = append(out, CodeAction{Title: item.Title, Kind: item.Kind})
	}
	return out, nil
}

// Diagnostics opens a document and waits briefly for publishDiagnostics.
func (c *Client) Diagnostics(ctx context.Context, path string) ([]Diagnostic, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	if err := c.openDocument(ctx, path); err != nil {
		return nil, err
	}
	for {
		msg, err := c.readMessageContext(ctx)
		if err != nil {
			return nil, err
		}
		var note struct {
			Method string `json:"method"`
			Params struct {
				URI         string          `json:"uri"`
				Diagnostics []lspDiagnostic `json:"diagnostics"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &note); err != nil || note.Method != "textDocument/publishDiagnostics" {
			continue
		}
		return normalizeDiagnostics(uriToPath(note.Params.URI), note.Params.Diagnostics), nil
	}
}

// CallHierarchy returns incoming and outgoing calls for a source position.
func (c *Client) CallHierarchy(ctx context.Context, path string, pos Position) ([]CallHierarchyItem, error) {
	if err := c.requireCapability(c.caps.CallHierarchy, "textDocument/prepareCallHierarchy"); err != nil {
		return nil, err
	}
	if err := c.openDocument(ctx, path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	params := map[string]any{"textDocument": map[string]any{"uri": pathToURI(path)}, "position": pos}
	raw, err := c.request(ctx, "textDocument/prepareCallHierarchy", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil, err
	}
	var prepared []lspCallHierarchyItem
	if err := json.Unmarshal(raw, &prepared); err != nil {
		return nil, fmt.Errorf("parse prepareCallHierarchy response: %w", err)
	}
	var out []CallHierarchyItem
	for _, item := range prepared {
		for _, method := range []string{"callHierarchy/incomingCalls", "callHierarchy/outgoingCalls"} {
			rawCalls, err := c.request(ctx, method, map[string]any{"item": item})
			if err != nil || len(rawCalls) == 0 || string(rawCalls) == "null" {
				continue
			}
			out = append(out, normalizeCalls(method, rawCalls)...)
		}
	}
	return out, nil
}

func (c *Client) openDocument(ctx context.Context, path string) error {
	// Tests can construct a Client around pipes without running initialize; skip
	// didOpen there so request-framing tests keep their expected first method.
	if !c.capOK {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	return c.notifyContext(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": pathToURI(path), "languageId": languageIDForPath(path), "version": 1, "text": readTextBestEffort(path)}})
}

func (c *Client) requireCapability(ok bool, method string) error {
	if !c.capOK || ok {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedCapability, method)
}

func (c *Client) locationRequest(ctx context.Context, method string, params any) ([]Location, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	raw, err := c.request(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var many []lspLocation
	if err := json.Unmarshal(raw, &many); err == nil {
		return normalizeLocations(many), nil
	}
	var one lspLocation
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("parse %s response: %w", method, err)
	}
	return normalizeLocations([]lspLocation{one}), nil
}

type lspLocation struct {
	URI   string `json:"uri"`
	Range struct {
		Start Position `json:"start"`
	} `json:"range"`
}

func normalizeLocations(in []lspLocation) []Location {
	out := make([]Location, 0, len(in))
	for _, loc := range in {
		out = append(out, Location{Path: uriToPath(loc.URI), Line: loc.Range.Start.Line, Character: loc.Range.Start.Character})
	}
	return out
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestLocked(ctx, method, params)
}

func (c *Client) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg, err := c.readMessageContext(ctx)
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		if resp.ID == 0 {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.notifyContext(context.Background(), method, params)
}

func (c *Client) notifyContext(ctx context.Context, method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.notifyLocked(method, params)
}

func (c *Client) notifyLocked(method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) writeMessage(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (c *Client) readMessageContext(ctx context.Context) ([]byte, error) {
	type result struct {
		body []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		body, err := c.readMessage()
		ch <- result{body: body, err: err}
	}()
	select {
	case res := <-ch:
		return res.body, res.err
	case <-ctx.Done():
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

func (c *Client) readMessage() ([]byte, error) {
	length := -1
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header: %w%s", err, c.stderrSuffix())
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid Content-Length %q", value)
		}
		length = n
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	if length > maxMessageBytes {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, fmt.Errorf("read body: %w%s", err, c.stderrSuffix())
	}
	return body, nil
}

func (c *Client) stderrSuffix() string {
	if c == nil || c.stderr.Len() == 0 {
		return ""
	}
	return "; stderr=" + strconv.Quote(c.stderr.String())
}

func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func uriToPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		return raw
	}
	return filepath.FromSlash(u.Path)
}

func symbolKindName(kind int) string {
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
		6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
		11: "interface", 12: "function", 13: "variable", 14: "constant", 15: "string",
		16: "number", 17: "boolean", 18: "array", 19: "object", 20: "key",
		21: "null", 22: "enumMember", 23: "struct", 24: "event", 25: "operator", 26: "typeParameter",
	}
	if name, ok := names[kind]; ok {
		return name
	}
	return fmt.Sprintf("kind%d", kind)
}

type cappedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.max {
		return len(p), nil
	}
	remaining := w.max - w.buf.Len()
	if len(p) > remaining {
		_, _ = w.buf.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = w.buf.Write(p)
	return len(p), nil
}
