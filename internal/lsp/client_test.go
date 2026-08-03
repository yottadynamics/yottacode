package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestClientJSONRPCFramingAndRequests(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{
		stdin:  clientWriter,
		stdout: bufio.NewReader(clientReader),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "workspace/symbol")
		writeServerResponse(t, serverWriter, 1, []map[string]any{{
			"name":          "Thing",
			"kind":          12,
			"containerName": "pkg",
			"location": map[string]any{
				"uri":   "file:///tmp/main.go",
				"range": map[string]any{"start": map[string]any{"line": 4, "character": 2}},
			},
		}})
	}()
	symbols, err := c.WorkspaceSymbols(context.Background(), "Thing")
	if err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}
	<-done
	if len(symbols) != 1 || symbols[0].Name != "Thing" || symbols[0].Kind != "function" || symbols[0].Location.Line != 4 {
		t.Fatalf("unexpected symbols: %+v", symbols)
	}
}

func TestClientLocationResponseAcceptsSingleLocation(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "textDocument/definition")
		writeServerResponse(t, serverWriter, 1, map[string]any{
			"uri":   "file:///tmp/main.go",
			"range": map[string]any{"start": map[string]any{"line": 1, "character": 7}},
		})
	}()
	locs, err := c.Definition(context.Background(), "/tmp/main.go", Position{})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	<-done
	if len(locs) != 1 || locs[0].Path != "/tmp/main.go" || locs[0].Line != 1 || locs[0].Character != 7 {
		t.Fatalf("unexpected locations: %+v", locs)
	}
}

func TestClientDocumentHighlightsResponse(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "textDocument/documentHighlight")
		writeServerResponse(t, serverWriter, 1, []map[string]any{{
			"range": map[string]any{
				"start": map[string]any{"line": 2, "character": 4},
				"end":   map[string]any{"line": 2, "character": 9},
			},
			"kind": 3,
		}})
	}()
	highlights, err := c.DocumentHighlights(context.Background(), "/tmp/main.go", Position{Line: 2, Character: 5})
	if err != nil {
		t.Fatalf("DocumentHighlights: %v", err)
	}
	<-done
	if len(highlights) != 1 || highlights[0].Kind != "write" || highlights[0].Range.Start.Line != 2 || highlights[0].Range.End.Character != 9 {
		t.Fatalf("unexpected highlights: %+v", highlights)
	}
}

func TestClientSelectionRangesResponse(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "textDocument/selectionRange")
		writeServerResponse(t, serverWriter, 1, []map[string]any{{
			"range": map[string]any{
				"start": map[string]any{"line": 4, "character": 8},
				"end":   map[string]any{"line": 4, "character": 12},
			},
			"parent": map[string]any{"range": map[string]any{
				"start": map[string]any{"line": 4, "character": 1},
				"end":   map[string]any{"line": 4, "character": 20},
			}},
		}})
	}()
	ranges, err := c.SelectionRanges(context.Background(), "/tmp/main.go", []Position{{Line: 4, Character: 9}})
	if err != nil {
		t.Fatalf("SelectionRanges: %v", err)
	}
	<-done
	if len(ranges) != 2 || ranges[0].Depth != 0 || ranges[1].Depth != 1 || ranges[1].Range.End.Character != 20 {
		t.Fatalf("unexpected selection ranges: %+v", ranges)
	}
}

func TestClientRenamePreviewPrepareRenameRunsBeforeRename(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader), capOK: true, caps: serverCapabilities{Rename: true, RenamePrepare: true}, docs: map[string]openDocumentState{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "textDocument/didOpen")
		readServerRequest(t, serverReader, "textDocument/prepareRename")
		writeServerResponse(t, serverWriter, 1, map[string]any{"range": map[string]any{"start": map[string]any{"line": 0, "character": 5}, "end": map[string]any{"line": 0, "character": 12}}, "placeholder": "oldName"})
		readServerRequest(t, serverReader, "textDocument/rename")
		writeServerResponse(t, serverWriter, 2, map[string]any{"changes": map[string]any{"file:///tmp/main.go": []map[string]any{{"range": map[string]any{"start": map[string]any{"line": 0, "character": 5}, "end": map[string]any{"line": 0, "character": 12}}, "newText": "newName"}}}})
	}()
	edit, err := c.RenamePreview(context.Background(), "/tmp/main.go", Position{Line: 0, Character: 5}, "newName")
	if err != nil {
		t.Fatalf("RenamePreview: %v", err)
	}
	<-done
	if len(edit.Edits) != 1 || edit.Edits[0].NewText != "newName" {
		t.Fatalf("unexpected rename edit: %+v", edit)
	}
}

func TestClientRenamePreviewPrepareRenameNullIsInvalid(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader), capOK: true, caps: serverCapabilities{Rename: true, RenamePrepare: true}, docs: map[string]openDocumentState{}}
	go func() {
		readServerRequest(t, serverReader, "textDocument/didOpen")
		readServerRequest(t, serverReader, "textDocument/prepareRename")
		writeServerResponse(t, serverWriter, 1, nil)
	}()
	_, err := c.RenamePreview(context.Background(), "/tmp/main.go", Position{}, "newName")
	if err == nil || !strings.Contains(err.Error(), ErrInvalidRenamePosition.Error()) {
		t.Fatalf("expected invalid rename position, got %v", err)
	}
}

func TestClientServerErrorSurfaces(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	go func() {
		readServerRequest(t, serverReader, "workspace/symbol")
		body := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`
		fmt.Fprintf(serverWriter, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}()
	_, err := c.WorkspaceSymbols(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "server error -32601: nope") {
		t.Fatalf("expected server error, got %v", err)
	}
}

func TestClientDiagnosticsReturnsCachedSnapshotImmediately(t *testing.T) {
	path := "/tmp/main.go"
	uri := pathToURI(path)
	c := &Client{docs: map[string]openDocumentState{}, diags: map[string]DiagnosticsSnapshot{uri: {Published: true, Diagnostics: []Diagnostic{{Path: path, Message: "cached"}}}}, diagSettle: time.Millisecond}
	snap, err := c.Diagnostics(context.Background(), path)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if !snap.Published || len(snap.Diagnostics) != 1 || snap.Diagnostics[0].Message != "cached" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestClientDiagnosticsCachesUnrelatedPublishWhileWaiting(t *testing.T) {
	path := "/tmp/main.go"
	var calls int
	c := &Client{
		capOK:      true,
		stdin:      nopWriteCloser{},
		docs:       map[string]openDocumentState{},
		diags:      map[string]DiagnosticsSnapshot{},
		diagSettle: 20 * time.Millisecond,
		readMessageFn: func() ([]byte, error) {
			calls++
			if calls == 1 {
				body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///tmp/other.go", "diagnostics": []map[string]any{{"range": map[string]any{"start": map[string]any{"line": 1, "character": 2}, "end": map[string]any{"line": 1, "character": 3}}, "message": "other", "severity": 2}}}})
				if err != nil {
					t.Fatalf("marshal diagnostics notification: %v", err)
				}
				return body, nil
			}
			ctxErrBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{"type": 3, "message": "idle"}})
			if err != nil {
				t.Fatalf("marshal idle notification: %v", err)
			}
			time.Sleep(25 * time.Millisecond)
			return ctxErrBody, nil
		},
	}
	snap, err := c.Diagnostics(context.Background(), path)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if snap.Published {
		t.Fatalf("expected unpublished snapshot for main.go, got %+v", snap)
	}
	other, ok := c.diags[pathToURI("/tmp/other.go")]
	if !ok || !other.Published || len(other.Diagnostics) != 1 || other.Diagnostics[0].Message != "other" {
		t.Fatalf("unexpected cached unrelated diagnostics: %+v", other)
	}
}

func TestClientDiagnosticsSerializesWithConcurrentRequests(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader), capOK: true, caps: serverCapabilities{WorkspaceSymbol: true}, docs: map[string]openDocumentState{}, diags: map[string]DiagnosticsSnapshot{}, diagSettle: 30 * time.Millisecond}

	diagDone := make(chan error, 1)
	go func() {
		snap, err := c.Diagnostics(context.Background(), "/tmp/main.go")
		if err != nil {
			diagDone <- err
			return
		}
		if !snap.Published {
			diagDone <- fmt.Errorf("diagnostics were not published: %+v", snap)
			return
		}
		diagDone <- nil
	}()
	readServerRequest(t, serverReader, "textDocument/didOpen")

	symbolStarted := make(chan struct{})
	symbolDone := make(chan error, 1)
	go func() {
		close(symbolStarted)
		items, err := c.WorkspaceSymbols(context.Background(), "Thing")
		if err != nil {
			symbolDone <- err
			return
		}
		if len(items) != 1 || items[0].Name != "Thing" {
			symbolDone <- fmt.Errorf("unexpected symbols: %+v", items)
			return
		}
		symbolDone <- nil
	}()
	<-symbolStarted

	writeServerNotification(t, serverWriter, "textDocument/publishDiagnostics", map[string]any{"uri": "file:///tmp/main.go", "diagnostics": []map[string]any{}})
	select {
	case err := <-diagDone:
		if err != nil {
			t.Fatalf("Diagnostics: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Diagnostics timed out")
	}

	readServerRequest(t, serverReader, "workspace/symbol")
	writeServerResponse(t, serverWriter, 1, []map[string]any{{
		"name":          "Thing",
		"kind":          12,
		"containerName": "pkg",
		"location": map[string]any{
			"uri":   "file:///tmp/main.go",
			"range": map[string]any{"start": map[string]any{"line": 4, "character": 2}},
		},
	}})
	select {
	case err := <-symbolDone:
		if err != nil {
			t.Fatalf("WorkspaceSymbols: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WorkspaceSymbols timed out")
	}
}

func TestClientReadMessageContextCancelsAndKillsProcess(t *testing.T) {
	cmd := sleepCmd(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c := &Client{
		cmd:    cmd,
		waitCh: make(chan error, 1),
		readMessageFn: func() ([]byte, error) {
			time.Sleep(100 * time.Millisecond)
			return nil, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.readMessageContext(ctx)
	if err == nil || !errors.Is(err, ErrRequestTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline timeout, got %v", err)
	}
	select {
	case <-c.processWaitCh():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for killed process")
	}
}

func TestPathURIConversion(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(cwd)
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("pathToURI should produce file URI, got %q", uri)
	}
	if got := uriToPath(uri); got == "" {
		t.Fatalf("uriToPath returned empty path")
	}
}

func readServerRequest(t *testing.T, r io.Reader, wantMethod string) {
	t.Helper()
	br := bufio.NewReader(r)
	length := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			if _, err := fmt.Sscanf(line, "Content-Length: %d", &length); err != nil {
				t.Fatalf("scan length: %v", err)
			}
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if msg.Method != wantMethod {
		t.Fatalf("method = %q, want %q; body=%s", msg.Method, wantMethod, body)
	}
}

func writeServerResponse(t *testing.T, w io.Writer, id int, result any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func writeServerNotification(t *testing.T, w io.Writer, method string, params any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func sleepCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err == nil {
		return exec.Command("/bin/sh", "-c", "sleep 30")
	}
	return exec.Command("sleep", "30")
}
