package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// InitializeTimeout is the wall-clock budget for the MCP initialize
// handshake. Slow servers don't block other servers (Manager.Start
// runs them concurrently); a per-server timeout keeps the picture
// bounded.
const InitializeTimeout = 5 * time.Second

// TerminateGracePeriod is how long the SDK's CommandTransport waits
// after closing stdin before escalating to SIGTERM/SIGKILL. Match the
// roadmap's 3s grace; the SDK then handles the signal escalation.
const TerminateGracePeriod = 3 * time.Second

// stderrBufLines is the rolling tail of subprocess stderr the client
// retains for `/mcp logs`. Lines beyond this are dropped from the head
// so a chatty server doesn't grow memory unbounded.
const stderrBufLines = 200

// clientName / clientVersion identify yottacode to MCP servers in the
// `initialize` handshake. clientVersion is overridable via build flags
// later — keeping it a const for v1.
const (
	clientName    = "yottacode"
	clientVersion = "0.0.0"
)

// StdioClient launches an MCP server as a stdio subprocess, performs
// the initialize handshake, and proxies tools/list + tools/call. Wraps
// the official Go SDK's CommandTransport — we don't re-implement
// JSON-RPC framing.
type StdioClient struct {
	// Config inputs (set once, read-only after construction):
	name    string
	command string
	args    []string
	env     map[string]string

	// warnings carries non-fatal config-time observations recorded at
	// construction (e.g. unresolved $VAR substitutions in env). Read
	// via Warnings() by the manager and surfaced through StartResult.
	warnings []string

	// Captured stderr for `/mcp logs <name>`. The SDK does NOT capture
	// stderr — we plug a buffer in directly via cmd.Stderr.
	stderr *ringBuffer

	// Live state (mutated only by Start / Stop, guarded by mu).
	mu         sync.Mutex
	session    *sdk.ClientSession
	procCancel context.CancelFunc // cancels the subprocess context; invoked after session.Close
	started    bool
	stopped    bool
	tools      []ToolDescriptor // cached after Start
}

// Warnings returns the (immutable post-construction) list of warnings
// recorded for this client — typically unresolved $VAR references in
// the configured env block. Surfaced by the manager via
// StartResult.Warnings so /mcp + run.go startup notices can show
// them.
func (c *StdioClient) Warnings() []string {
	out := make([]string, len(c.warnings))
	copy(out, c.warnings)
	return out
}

// NewStdioClient constructs a StdioClient. The subprocess is NOT
// spawned here — Start does that. env values may contain $VAR
// references that get resolved against the process environment at
// Start time.
func NewStdioClient(name, command string, args []string, env map[string]string) *StdioClient {
	c := &StdioClient{
		name:    name,
		command: command,
		args:    args,
		env:     env,
		stderr:  newRingBuffer(stderrBufLines),
	}
	c.warnings = envExpansionWarnings(name, env)
	return c
}

// envExpansionWarnings probes the env block for $VAR references whose
// targets aren't set in yottacode's process environment. Returns a
// human-readable warning per missing variable (config key + var
// name). Empty when nothing is missing.
func envExpansionWarnings(name string, env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	var out []string
	for k, v := range env {
		for _, varName := range referencedVars(v) {
			if _, set := os.LookupEnv(varName); !set {
				out = append(out, fmt.Sprintf("mcp(%s): env %s references $%s which is unset; subprocess will receive empty string",
					name, k, varName))
			}
		}
	}
	return out
}

// referencedVars extracts every $NAME or ${NAME} reference from a
// string in the order they appear. Mirrors os.Expand's recognized
// shape but reports the names instead of substituting.
func referencedVars(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		j := i + 1
		if j >= len(s) {
			return out
		}
		if s[j] == '{' {
			end := strings.IndexByte(s[j+1:], '}')
			if end < 0 {
				return out
			}
			out = append(out, s[j+1:j+1+end])
			i = j + 1 + end
			continue
		}
		k := j
		for k < len(s) && (isAlnumUnderscore(s[k])) {
			k++
		}
		if k > j {
			out = append(out, s[j:k])
		}
		i = k - 1
	}
	return out
}

func isAlnumUnderscore(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_'
}

// Name returns the configured server name.
func (c *StdioClient) Name() string { return c.name }

// Start resolves the command via exec.LookPath, spawns the subprocess,
// and performs the MCP initialize handshake. Bound by InitializeTimeout
// regardless of the caller-supplied ctx — slow servers don't hang the
// whole session.
func (c *StdioClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return fmt.Errorf("mcp(%s): already started", c.name)
	}

	bin, err := exec.LookPath(c.command)
	if err != nil {
		return fmt.Errorf("mcp(%s): command %q not found in PATH: %w", c.name, c.command, err)
	}

	initCtx, cancelInit := context.WithTimeout(ctx, InitializeTimeout)
	defer cancelInit()

	// The subprocess context outlives Start; it's cancelled by Stop
	// only after session.Close has done its graceful-shutdown ladder.
	// Using initCtx here would SIGKILL the subprocess the moment
	// Start returns.
	procCtx, procCancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(procCtx, bin, c.args...)
	cmd.Env = mergeEnv(os.Environ(), c.env)
	cmd.Stderr = c.stderr
	setProcAttr(cmd)

	client := sdk.NewClient(&sdk.Implementation{
		Name:    clientName,
		Version: clientVersion,
	}, nil)

	transport := &sdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: TerminateGracePeriod,
	}

	session, err := client.Connect(initCtx, transport, nil)
	if err != nil {
		procCancel()
		return fmt.Errorf("mcp(%s): connect: %w", c.name, err)
	}

	c.session = session
	c.procCancel = procCancel
	c.started = true

	// Eager catalog fetch — the agent registry needs the descriptors
	// at session start anyway, and any tools/list failure during
	// Start surfaces as a startup error (which the manager renders
	// per-server) rather than at first tool invocation.
	tools, err := c.fetchTools(initCtx)
	if err != nil {
		_ = session.Close()
		procCancel()
		c.session = nil
		c.procCancel = nil
		c.started = false
		return fmt.Errorf("mcp(%s): list tools: %w", c.name, err)
	}
	c.tools = tools
	return nil
}

// ListTools returns the cached catalog from Start. Returns an error if
// Start hasn't been called or already failed.
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil, ErrNotStarted
	}
	// Copy to defend against caller mutation; tools slice rarely
	// changes after Start so the allocation is fine.
	out := make([]ToolDescriptor, len(c.tools))
	copy(out, c.tools)
	return out, nil
}

// fetchTools queries tools/list and translates the SDK's Tool shape
// into our transport-agnostic ToolDescriptor. Must be called with c.mu
// held by Start (the eager fetch path) or unlocked from a request
// goroutine in the future when we add live refresh.
func (c *StdioClient) fetchTools(ctx context.Context) ([]ToolDescriptor, error) {
	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ToolDescriptor, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		schema, _ := toSchemaMap(t.InputSchema)
		readOnly := false
		if t.Annotations != nil {
			readOnly = t.Annotations.ReadOnlyHint
		}
		out = append(out, ToolDescriptor{
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  schema,
			ReadOnlyHint: readOnly,
		})
	}
	return out, nil
}

// CallTool invokes the named tool with the raw JSON arguments payload.
// argsJSON is the literal payload the model emitted; we unmarshal into
// map[string]any and pass through to the SDK.
func (c *StdioClient) CallTool(ctx context.Context, toolName, argsJSON string) (CallResult, error) {
	c.mu.Lock()
	session := c.session
	started := c.started
	c.mu.Unlock()

	if !started || session == nil {
		return CallResult{}, ErrNotStarted
	}

	var args map[string]any
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return CallResult{}, fmt.Errorf("mcp(%s/%s): invalid argument JSON: %w",
				c.name, toolName, err)
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp(%s/%s): %w", c.name, toolName, err)
	}

	return CallResult{
		Text:    extractText(res),
		IsError: res.IsError,
	}, nil
}

// Stop closes the SDK session, which closes the subprocess stdin and
// triggers the graceful-shutdown ladder (close → wait → SIGTERM → kill)
// implemented in the SDK's pipeRWC. The subprocess-lifetime context is
// then cancelled as a backstop in case session.Close left a zombie.
// Idempotent — repeat calls return nil.
func (c *StdioClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	session := c.session
	procCancel := c.procCancel
	alreadyStopped := c.stopped
	c.stopped = true
	c.session = nil
	c.procCancel = nil
	c.started = false
	c.mu.Unlock()

	if alreadyStopped {
		return nil
	}
	var err error
	if session != nil {
		err = session.Close()
	}
	if procCancel != nil {
		procCancel()
	}
	return err
}

// StderrTail returns the recent stderr lines captured from the
// subprocess. Exposed for the /mcp logs subcommand.
func (c *StdioClient) StderrTail() []string {
	return c.stderr.Lines()
}

// extractText flattens a CallToolResult into the single text string
// the agent.Tool.Execute contract expects. Text content blocks pass
// through verbatim; non-text blocks (image / audio / resource link /
// embedded resource) become explicit placeholder markers so the model
// learns that *something* was returned but yottacode's v1 bridge
// dropped it — far better than silently returning an empty string,
// which would make the model retry the same call or hallucinate
// content.
//
// StructuredContent is used as a fallback when no TextContent blocks
// were present. The SDK's typed-output handler path auto-populates
// Content with a stringified copy of StructuredContent, so this
// branch only matters when a server populates StructuredContent
// directly and leaves Content empty or non-textual.
//
// Multi-block results are joined with newlines.
func extractText(res *sdk.CallToolResult) string {
	if res == nil {
		return ""
	}
	parts := make([]string, 0, len(res.Content))
	textSeen := false
	for _, c := range res.Content {
		switch tc := c.(type) {
		case *sdk.TextContent:
			if tc != nil {
				parts = append(parts, tc.Text)
				textSeen = true
			}
		case *sdk.ImageContent:
			if tc != nil {
				parts = append(parts, fmt.Sprintf(
					"[image omitted: %s, %d bytes — yottacode v1 tools-only bridge passes text only]",
					orDefault(tc.MIMEType, "image/unknown"), len(tc.Data)))
			}
		case *sdk.AudioContent:
			if tc != nil {
				parts = append(parts, fmt.Sprintf(
					"[audio omitted: %s, %d bytes — yottacode v1 tools-only bridge passes text only]",
					orDefault(tc.MIMEType, "audio/unknown"), len(tc.Data)))
			}
		case *sdk.ResourceLink:
			if tc != nil {
				parts = append(parts, fmt.Sprintf(
					"[resource link omitted: %s (%s) — yottacode v1 does not fetch MCP resources]",
					tc.URI, orDefault(tc.MIMEType, "unknown")))
			}
		case *sdk.EmbeddedResource:
			if tc != nil {
				var uri string
				if tc.Resource != nil {
					uri = tc.Resource.URI
				}
				parts = append(parts, fmt.Sprintf(
					"[embedded resource omitted: %s — yottacode v1 does not fetch MCP resources]",
					orDefault(uri, "<no uri>")))
			}
		default:
			// Future-proof: a content type the SDK adds later
			// (or a custom one we haven't matched) still surfaces
			// as a marker rather than vanishing.
			if c != nil {
				parts = append(parts, fmt.Sprintf("[unsupported content type %T omitted]", c))
			}
		}
	}
	if res.StructuredContent != nil && !textSeen {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// toSchemaMap normalizes the SDK's `any`-typed InputSchema (which is
// json.RawMessage when received from a server) into the map[string]any
// shape the rest of yottacode expects. Falls back to an empty schema
// when the server omitted one — the model will refuse to call the tool
// without args, which is the right failure mode.
func toSchemaMap(raw any) (map[string]any, bool) {
	if raw == nil {
		return map[string]any{}, false
	}
	switch v := raw.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err == nil {
			return m, true
		}
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err == nil {
			return m, true
		}
	}
	// Last-resort: round-trip via json — covers the SDK's typed
	// jsonschema.Schema struct without us importing the schema lib.
	b, err := json.Marshal(raw)
	if err == nil {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err == nil {
			return m, true
		}
	}
	return map[string]any{}, false
}

// mergeEnv combines the inherited environment with per-server overrides.
// Values in extra may contain $VAR references; they're expanded against
// the inherited env using os.Expand semantics. Unresolved $VARs are
// left as the literal string (the subprocess will get a missing-env
// error, which surfaces clearly in its stderr).
func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	resolver := func(k string) string { return os.Getenv(k) }
	// Build a map keyed by var name for override semantics, then
	// flatten back to the os/exec K=V slice shape.
	overrides := make(map[string]string, len(extra))
	for k, v := range extra {
		overrides[k] = os.Expand(v, resolver)
	}
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			out = append(out, kv)
			continue
		}
		k := kv[:eq]
		if v, ok := overrides[k]; ok {
			out = append(out, k+"="+v)
			seen[k] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// ringBuffer captures subprocess stderr in a bounded sliding window of
// lines. It implements io.Writer so exec.Cmd can write directly into
// it.
type ringBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
	buf   bytes.Buffer
}

func newRingBuffer(maxLines int) *ringBuffer {
	if maxLines <= 0 {
		maxLines = 1
	}
	return &ringBuffer{max: maxLines, lines: make([]string, 0, maxLines)}
}

// Write accumulates bytes and splits them into lines as newlines arrive.
// Partial trailing lines stay buffered until the next Write completes
// them — same shape as a typical line-buffered tail.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, _ := r.buf.Write(p)
	for {
		line, err := r.buf.ReadString('\n')
		if err == io.EOF {
			// Incomplete trailing line — push it back so the next
			// Write can complete it. ReadString already consumed it.
			if line != "" {
				// Re-buffer the partial line at the head.
				r.buf.WriteString(line)
				// Put back via a tmp because Buffer prepend is awkward;
				// re-write and re-read on next call.
				rest := r.buf.Bytes()
				r.buf.Reset()
				r.buf.Write(rest)
			}
			return n, nil
		}
		r.push(strings.TrimRight(line, "\n"))
	}
}

func (r *ringBuffer) push(line string) {
	if len(r.lines) >= r.max {
		// Drop oldest. Slice the head off — small max (200) keeps this
		// cheap. Switch to a real ring if max grows beyond ~10k.
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.max-1]
	}
	r.lines = append(r.lines, line)
}

// Lines returns a snapshot of the captured lines, oldest first.
func (r *ringBuffer) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}
