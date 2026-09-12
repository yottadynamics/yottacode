package mcp

import (
	"bytes"
	"context"

	"errors"

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
const InitializeTimeout = 30 * time.Second

// TerminateGracePeriod is how long the SDK's CommandTransport waits
// after closing stdin before escalating to SIGTERM/SIGKILL. Match the
// roadmap's 3s grace; the SDK then handles the signal escalation.
const TerminateGracePeriod = 3 * time.Second

// stderrBufLines is the rolling tail of subprocess stderr the client
// retains for `/mcp logs`. Lines beyond this are dropped from the head
// so a chatty server doesn't grow memory unbounded.
const stderrBufLines = 200

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

	// Live state (mutated only by Start / Stop, guarded by ops.mu).
	ops        sessionOps
	procCancel context.CancelFunc // cancels the subprocess context; invoked after session.Close
	stopped    bool
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
	c.ops.mu.Lock()
	if c.ops.started || c.ops.starting {
		c.ops.mu.Unlock()
		return fmt.Errorf("mcp(%s): already started", c.name)
	}
	if c.stopped {
		c.ops.mu.Unlock()
		return fmt.Errorf("mcp(%s): client is stopped", c.name)
	}
	c.ops.starting = true
	c.ops.mu.Unlock()
	// This block is a merge-conflict-resolution fix, not a new
	// mechanism: main's Start (see internal/mcp/stdio_client.go on
	// main, added by #321 "Harden MCP transports and lifecycle
	// management") already locks only for this check-and-set, then
	// unlocks before the slow work below — bind and fetchTools each
	// take c.ops.mu themselves, and it is NOT reentrant, so holding it
	// across those calls self-deadlocks the goroutine the moment bind
	// runs, and every return before that point leaks the lock forever,
	// wedging every future ListTools/CallTool/Stop on this client. This
	// branch's own `stopped` check got added against an older,
	// whole-function-locked version of Start, and merging main back in
	// silently lost main's fix while keeping the old locking shape.
	// This restores main's pattern with the stopped check folded in.
	defer func() {
		c.ops.mu.Lock()
		if !c.ops.started {
			c.ops.starting = false
		}
		c.ops.mu.Unlock()
	}()

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
		Version: clientVersion(),
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

	c.ops.bind(session)
	c.procCancel = procCancel

	// Eager catalog fetch — the agent registry needs the descriptors
	// at session start anyway, and any tools/list failure during
	// Start surfaces as a startup error (which the manager renders
	// per-server) rather than at first tool invocation.
	if _, err := c.ops.fetchTools(initCtx); err != nil {
		_ = session.Close()
		procCancel()
		c.ops.clear()
		c.procCancel = nil
		return fmt.Errorf("mcp(%s): list tools: %w", c.name, err)
	}
	return nil
}

// ListTools returns the cached catalog from Start. Returns an error if
// Start hasn't been called or already failed.
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	c.ops.mu.RLock()
	defer c.ops.mu.RUnlock()
	return c.ops.listTools()
}

// RefreshTools re-queries tools/list and replaces the cached catalog.
func (c *StdioClient) RefreshTools(ctx context.Context) ([]ToolDescriptor, error) {
	return c.ops.fetchTools(ctx)
}

// CallTool invokes the named tool with the raw JSON arguments payload.
func (c *StdioClient) CallTool(ctx context.Context, toolName, argsJSON string) (CallResult, error) {
	return c.ops.callTool(ctx, c.name, toolName, argsJSON)
}

// Stop closes the SDK session, which closes the subprocess stdin and
// triggers the graceful-shutdown ladder (close → wait → SIGTERM → kill)
// implemented in the SDK's pipeRWC. The subprocess-lifetime context is
// canceled first so child teardown begins before the potentially blocking
// protocol/session close. Idempotent — repeat calls return nil.
func (c *StdioClient) Stop(ctx context.Context) error {
	c.ops.mu.Lock()
	session := c.ops.session
	procCancel := c.procCancel
	alreadyStopped := c.stopped
	c.stopped = true
	c.procCancel = nil
	c.ops.mu.Unlock()
	c.ops.clear()

	if alreadyStopped {
		return nil
	}
	// Cancel the child before protocol close: SDK Close can wait on a peer that
	// never responds, while cancellation immediately starts process teardown.
	if procCancel != nil {
		procCancel()
	}
	if session == nil {
		return nil
	}
	errCh := make(chan error, 1)
	go func() { errCh <- session.Close() }()
	select {
	case err := <-errCh:
		if procCancel != nil && isExpectedShutdownErr(err) {
			// Canceling the child before the protocol close races the SDK's
			// graceful-shutdown ladder: session.Close may observe the child
			// already torn down and report context.Canceled, a signal kill, or
			// another cancellation-shaped error. Those are the expected outcome
			// of the cancel-first ordering, not transport failures to surface.
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isExpectedShutdownErr reports whether err is a cancellation/kill the
// cancel-first Stop ordering deliberately produces during teardown, as
// opposed to a transport failure worth returning to the caller. A nil
// err is a clean shutdown and reports true so callers can branch on a
// single gate.
func isExpectedShutdownErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// The SDK can surface the subprocess kill as a non-sentinel error string.
	return strings.Contains(err.Error(), "signal: killed")
}

// LogTail returns the recent stderr lines captured from the subprocess.
// Satisfies mcp.LogSource so /mcp logs works uniformly across transports.
func (c *StdioClient) LogTail() []string {
	return c.stderr.Lines()
}

// mergeEnv combines the inherited environment with per-server overrides.
// Values in extra may contain $VAR references; os.Expand substitutes unset
// variables with the empty string, and envExpansionWarnings surfaces those
// missing references before the subprocess starts.
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
