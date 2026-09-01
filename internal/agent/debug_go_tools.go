package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	godap "github.com/google/go-dap"
	"github.com/yottadynamics/yottacode/internal/dap"
)

const (
	debugDefaultRequestTimeout  = 5 * time.Second
	debugDefaultContinueTimeout = 30 * time.Second
	debugMaxOutputBytes         = 64 * 1024
)

var (
	debugLookPath       = exec.LookPath
	debugCommandContext = exec.CommandContext
	debugDialContext    = (&net.Dialer{}).DialContext
)

type goDebugClient interface {
	Initialize(context.Context, godap.InitializeRequestArguments) (godap.Capabilities, error)
	Launch(context.Context, map[string]any) error
	SetBreakpoints(context.Context, godap.SetBreakpointsArguments) (godap.SetBreakpointsResponseBody, error)
	ConfigurationDone(context.Context) error
	Continue(context.Context, godap.ContinueArguments) (godap.ContinueResponseBody, error)
	Next(context.Context, godap.NextArguments) error
	StepIn(context.Context, godap.StepInArguments) error
	StepOut(context.Context, godap.StepOutArguments) error
	StackTrace(context.Context, godap.StackTraceArguments) (godap.StackTraceResponseBody, error)
	Scopes(context.Context, godap.ScopesArguments) (godap.ScopesResponseBody, error)
	Variables(context.Context, godap.VariablesArguments) (godap.VariablesResponseBody, error)
	Evaluate(context.Context, godap.EvaluateArguments) (godap.EvaluateResponseBody, error)
	Disconnect(context.Context, godap.DisconnectArguments) error
	Events() <-chan godap.EventMessage
	Close(context.Context) error
}

type goDebugSession struct {
	client goDebugClient
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stderr *cappedString
}

// goDebugManager keeps the v1 debugger scope deliberately small: one approved
// Delve-backed debug session per yottacode session.
type goDebugManager struct {
	mu         sync.Mutex
	cwd        *CwdRef
	session    *goDebugSession
	threadID   int
	configured bool
	stopped    bool
	start      func(context.Context, *CwdRef, debugStartArgs) (*goDebugSession, error)
}

func newGoDebugManager(cwd *CwdRef) *goDebugManager {
	return &goDebugManager{cwd: cwd, start: startDelveDAPSession}
}

type debugStartArgs struct {
	Mode    string   `json:"mode"`
	Program string   `json:"program"`
	Package string   `json:"package"`
	Args    []string `json:"args"`
}

type debugBreakpointArgs struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type debugStepArgs struct {
	Kind     string `json:"kind"`
	ThreadID int    `json:"thread_id"`
}

type debugStackArgs struct {
	ThreadID int `json:"thread_id"`
	Levels   int `json:"levels"`
}

type debugVarsArgs struct {
	FrameID            int    `json:"frame"`
	Filter             string `json:"filter"`
	VariablesReference int    `json:"variables_reference"`
	Count              int    `json:"count"`
}

type debugEvalArgs struct {
	Expression string `json:"expression"`
	FrameID    int    `json:"frame"`
	Context    string `json:"context"`
}

func (m *goDebugManager) startSession(ctx context.Context, a debugStartArgs) (string, error) {
	mode := strings.TrimSpace(a.Mode)
	if mode == "" {
		mode = "test"
	}
	if mode != "launch" && mode != "test" {
		return "", fmt.Errorf("debug_start: mode must be launch or test")
	}
	program := strings.TrimSpace(a.Program)
	if program == "" {
		program = strings.TrimSpace(a.Package)
	}
	if program == "" {
		return "", fmt.Errorf("debug_start: program or package is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		return "", fmt.Errorf("debug_start: a Go debug session is already running; call debug_stop first")
	}
	started, err := m.start(ctx, m.cwd, debugStartArgs{Mode: mode, Program: program, Args: a.Args})
	if err != nil {
		return "", err
	}
	m.session = started
	m.threadID = 0
	m.configured = false
	m.stopped = false

	if _, err := started.client.Initialize(ctx, godap.InitializeRequestArguments{AdapterID: "go", LinesStartAt1: true, ColumnsStartAt1: true}); err != nil {
		_ = started.close(ctx)
		m.session = nil
		return "", fmt.Errorf("debug_start: initialize Delve DAP: %w", err)
	}
	launchArgs := map[string]any{"mode": modeToDelveMode(mode), "program": program}
	if len(a.Args) > 0 {
		launchArgs["args"] = a.Args
	}
	if err := started.client.Launch(ctx, launchArgs); err != nil {
		_ = started.close(ctx)
		m.session = nil
		return "", fmt.Errorf("debug_start: launch Delve DAP session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = started.close(context.Background())
		m.session = nil
		m.configured = false
		return "", fmt.Errorf("debug_start: %w", err)
	}
	return fmt.Sprintf("started Go debug session mode=%s program=%s", mode, program), nil
}

func modeToDelveMode(mode string) string {
	if mode == "test" {
		return "test"
	}
	return "debug"
}

func (m *goDebugManager) active() (*goDebugSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return nil, fmt.Errorf("no active Go debug session; call debug_start first")
	}
	return m.session, nil
}

func (m *goDebugManager) ensureConfigurationDone(ctx context.Context, sess *goDebugSession) error {
	m.mu.Lock()
	if m.configured {
		m.mu.Unlock()
		return nil
	}
	m.configured = true
	m.mu.Unlock()
	if err := sess.client.ConfigurationDone(ctx); err != nil {
		m.mu.Lock()
		m.configured = false
		m.mu.Unlock()
		return err
	}
	return nil
}

func (m *goDebugManager) markStoppedThread(id int) {
	if id == 0 {
		return
	}
	m.mu.Lock()
	m.threadID = id
	m.stopped = true
	m.mu.Unlock()
}

func (m *goDebugManager) markRunning() {
	m.mu.Lock()
	m.stopped = false
	m.mu.Unlock()
}

func (m *goDebugManager) shouldSendContinue(providedThreadID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configured && m.stopped && (providedThreadID != 0 || m.threadID != 0)
}

func (m *goDebugManager) markEnded() {
	m.mu.Lock()
	m.session = nil
	m.threadID = 0
	m.configured = false
	m.stopped = false
	m.mu.Unlock()
}

func (m *goDebugManager) currentThread(provided int) int {
	if provided != 0 {
		return provided
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.threadID != 0 {
		return m.threadID
	}
	return 1
}

func (m *goDebugManager) stop(ctx context.Context) (string, error) {
	m.mu.Lock()
	sess := m.session
	m.session = nil
	m.threadID = 0
	m.configured = false
	m.mu.Unlock()
	if sess == nil {
		return "no active Go debug session", nil
	}
	if err := sess.close(ctx); err != nil {
		return "", fmt.Errorf("debug_stop: %w", err)
	}
	return "stopped Go debug session", nil
}

// Cleanup closes a live debug session without requiring the caller to know
// whether one exists. Runtime shutdown uses this to avoid orphaning Delve.
func (m *goDebugManager) Cleanup(ctx context.Context) error {
	_, err := m.stop(ctx)
	return err
}

func (s *goDebugSession) close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	_ = s.client.Disconnect(ctx, godap.DisconnectArguments{})
	_ = s.client.Close(ctx)
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if s.cmd.ProcessState != nil && s.cmd.ProcessState.Exited() {
		return nil
	}
	_ = s.cmd.Process.Kill()
	if err := s.cmd.Wait(); err != nil && s.stderr != nil && s.stderr.String() != "" {
		return fmt.Errorf("Delve exited: %w: %s", err, s.stderr.String())
	}
	return nil
}

func startDelveDAPSession(ctx context.Context, cwd *CwdRef, _ debugStartArgs) (*goDebugSession, error) {
	dlv, err := debugLookPath("dlv")
	if err != nil {
		return nil, fmt.Errorf("debug_start: dlv not found in PATH; install Delve first (for Go: go install github.com/go-delve/delve/cmd/dlv@latest)")
	}
	processCtx, cancel := context.WithCancel(context.Background())
	cmd := debugCommandContext(processCtx, dlv, "dap", "--listen=127.0.0.1:0")
	cmd.Dir = cwd.Get()
	stderr := &cappedString{max: debugMaxOutputBytes}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("debug_start: open dlv stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("debug_start: start dlv dap: %w", err)
	}
	addr, err := waitForDelveDAPAddress(ctx, stdout, stderr)
	if err != nil {
		cancel()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	conn, err := debugDialContext(ctx, "tcp", addr)
	if err != nil {
		cancel()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("debug_start: connect to dlv dap at %s: %w", addr, err)
	}
	client, err := dap.NewClient(conn, dap.ClientOptions{RequestTimeout: debugDefaultRequestTimeout})
	if err != nil {
		_ = conn.Close()
		cancel()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("debug_start: create DAP client: %w", err)
	}
	return &goDebugSession{client: client, cmd: cmd, cancel: cancel, stderr: stderr}, nil
}

func waitForDelveDAPAddress(ctx context.Context, stdout ioReader, stderr fmt.Stringer) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, debugDefaultRequestTimeout)
	defer cancel()
	lines := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				for {
					text := acc.String()
					line, rest, ok := strings.Cut(text, "\n")
					if !ok {
						break
					}
					line = strings.TrimSpace(line)
					acc.Reset()
					acc.WriteString(rest)
					if strings.Contains(line, "DAP server listening at:") {
						lines <- strings.TrimSpace(strings.TrimPrefix(line, "DAP server listening at:"))
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case addr := <-lines:
		if addr == "" {
			return "", fmt.Errorf("debug_start: dlv dap did not report a listen address")
		}
		return addr, nil
	case <-deadline.Done():
		return "", fmt.Errorf("debug_start: timed out waiting for dlv dap to start: %s", stderr.String())
	}
}

type ioReader interface{ Read([]byte) (int, error) }

type cappedString struct {
	mu  sync.Mutex
	buf strings.Builder
	max int
}

func (c *cappedString) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max <= 0 || c.buf.Len() < c.max {
		remaining := c.max - c.buf.Len()
		if c.max <= 0 || remaining > len(p) {
			remaining = len(p)
		}
		c.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func (c *cappedString) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

type goDebugTool struct {
	manager *goDebugManager
	name    string
}

func newGoDebugTools(cwd *CwdRef) []Tool {
	m := newGoDebugManager(cwd)
	return []Tool{
		&DebugStartTool{goDebugTool: goDebugTool{manager: m, name: "debug_start"}},
		&DebugBreakpointTool{goDebugTool: goDebugTool{manager: m, name: "debug_breakpoint"}},
		&DebugContinueTool{goDebugTool: goDebugTool{manager: m, name: "debug_continue"}},
		&DebugStepTool{goDebugTool: goDebugTool{manager: m, name: "debug_step"}},
		&DebugStackTool{goDebugTool: goDebugTool{manager: m, name: "debug_stack"}},
		&DebugVarsTool{goDebugTool: goDebugTool{manager: m, name: "debug_vars"}},
		&DebugEvalTool{goDebugTool: goDebugTool{manager: m, name: "debug_eval"}},
		&DebugStopTool{goDebugTool: goDebugTool{manager: m, name: "debug_stop"}},
	}
}

func (t goDebugTool) Name() string { return t.name }

func (t goDebugTool) Cleanup(ctx context.Context) error {
	return t.manager.Cleanup(ctx)
}

// DebugStartTool starts one Delve DAP session. It always requires approval
// because it executes the user's program or tests.
type DebugStartTool struct{ goDebugTool }

func (t *DebugStartTool) Description() string {
	return "Start one Go debug session through dlv dap. Requires dlv on PATH and never downloads binaries."
}
func (t *DebugStartTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"mode":    map[string]any{"type": "string", "enum": []string{"launch", "test"}, "description": "Debug mode: launch or test"},
		"program": map[string]any{"type": "string", "description": "Program path or package to debug"},
		"package": map[string]any{"type": "string", "description": "Package to test-debug; alias for program"},
		"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Program or test arguments"},
	}, "required": []string{"mode"}}
}
func (t *DebugStartTool) RequiresApproval(string) bool { return true }
func (t *DebugStartTool) PreviewCall(argsJSON string) string {
	var a debugStartArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("debug_start(mode=%s program=%s package=%s args=%d)", a.Mode, a.Program, a.Package, len(a.Args))
}
func (t *DebugStartTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugStartArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("debug_start: invalid args: %w", err)
	}
	return t.manager.startSession(ctx, a)
}

type DebugBreakpointTool struct{ goDebugTool }

func (t *DebugBreakpointTool) Description() string {
	return "Set a Go debug breakpoint by file and line."
}
func (t *DebugBreakpointTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"file": map[string]any{"type": "string", "description": "Source file"},
		"line": map[string]any{"type": "integer", "description": "1-based source line"},
	}, "required": []string{"file", "line"}}
}
func (t *DebugBreakpointTool) RequiresApproval(string) bool { return false }
func (t *DebugBreakpointTool) PreviewCall(argsJSON string) string {
	var a debugBreakpointArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("debug_breakpoint(%s:%d)", a.File, a.Line)
}
func (t *DebugBreakpointTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugBreakpointArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("debug_breakpoint: invalid args: %w", err)
	}
	if strings.TrimSpace(a.File) == "" || a.Line <= 0 {
		return "", fmt.Errorf("debug_breakpoint: file and positive line are required")
	}
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	path := resolvePath(t.manager.cwd.Get(), a.File)
	body, err := sess.client.SetBreakpoints(ctx, godap.SetBreakpointsArguments{Source: godap.Source{Name: filepath.Base(path), Path: path}, Breakpoints: []godap.SourceBreakpoint{{Line: a.Line}}})
	if err != nil {
		return "", fmt.Errorf("debug_breakpoint: %w", err)
	}
	return formatBreakpoints(body.Breakpoints), nil
}

type DebugContinueTool struct{ goDebugTool }

func (t *DebugContinueTool) Description() string {
	return "Continue the active Go debug session and wait up to 30s for the next stop event."
}
func (t *DebugContinueTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"thread_id": map[string]any{"type": "integer", "description": "Thread id; defaults to last stopped thread"}}}
}
func (t *DebugContinueTool) RequiresApproval(string) bool       { return false }
func (t *DebugContinueTool) PreviewCall(argsJSON string) string { return "debug_continue()" }
func (t *DebugContinueTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		ThreadID int `json:"thread_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	if err := t.manager.ensureConfigurationDone(ctx, sess); err != nil {
		return "", fmt.Errorf("debug_continue: configurationDone: %w", err)
	}
	if t.manager.shouldSendContinue(a.ThreadID) {
		if _, err := sess.client.Continue(ctx, godap.ContinueArguments{ThreadId: t.manager.currentThread(a.ThreadID)}); err != nil {
			return "", fmt.Errorf("debug_continue: %w", err)
		}
		t.manager.markRunning()
	}
	return t.manager.waitForStop(ctx, debugDefaultContinueTimeout)
}

type DebugStepTool struct{ goDebugTool }

func (t *DebugStepTool) Description() string {
	return "Step the active Go debug session: next, stepIn, or stepOut."
}
func (t *DebugStepTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"kind":      map[string]any{"type": "string", "enum": []string{"next", "stepIn", "stepOut"}, "description": "Step operation (default next)"},
		"thread_id": map[string]any{"type": "integer", "description": "Thread id; defaults to last stopped thread"},
	}}
}
func (t *DebugStepTool) RequiresApproval(string) bool { return false }
func (t *DebugStepTool) PreviewCall(argsJSON string) string {
	var a debugStepArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Kind == "" {
		a.Kind = "next"
	}
	return "debug_step(" + a.Kind + ")"
}
func (t *DebugStepTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugStepArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("debug_step: invalid args: %w", err)
	}
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	if err := t.manager.ensureConfigurationDone(ctx, sess); err != nil {
		return "", fmt.Errorf("debug_step: configurationDone: %w", err)
	}
	threadID := t.manager.currentThread(a.ThreadID)
	switch a.Kind {
	case "", "next":
		err = sess.client.Next(ctx, godap.NextArguments{ThreadId: threadID})
	case "stepIn":
		err = sess.client.StepIn(ctx, godap.StepInArguments{ThreadId: threadID})
	case "stepOut":
		err = sess.client.StepOut(ctx, godap.StepOutArguments{ThreadId: threadID})
	default:
		return "", fmt.Errorf("debug_step: kind must be next, stepIn, or stepOut")
	}
	if err != nil {
		return "", fmt.Errorf("debug_step: %w", err)
	}
	t.manager.markRunning()
	return t.manager.waitForStop(ctx, debugDefaultContinueTimeout)
}

func (m *goDebugManager) waitForStop(ctx context.Context, timeout time.Duration) (string, error) {
	sess, err := m.active()
	if err != nil {
		return "", err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sess.client.Events():
			if !ok {
				return "debug session ended", nil
			}
			switch e := ev.(type) {
			case *godap.StoppedEvent:
				m.markStoppedThread(e.Body.ThreadId)
				return fmt.Sprintf("stopped: reason=%s thread=%d", e.Body.Reason, e.Body.ThreadId), nil
			case *godap.TerminatedEvent:
				m.markEnded()
				return "debug session terminated", nil
			case *godap.ExitedEvent:
				m.markEnded()
				return fmt.Sprintf("debuggee exited with code %d", e.Body.ExitCode), nil
			}
		case <-timer.C:
			return "still running after 30s", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

type DebugStackTool struct{ goDebugTool }

func (t *DebugStackTool) Description() string {
	return "Return stack frames for the active Go debug session."
}
func (t *DebugStackTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"thread_id": map[string]any{"type": "integer"}, "levels": map[string]any{"type": "integer"}}}
}
func (t *DebugStackTool) RequiresApproval(string) bool { return false }
func (t *DebugStackTool) PreviewCall(string) string    { return "debug_stack()" }
func (t *DebugStackTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugStackArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	body, err := sess.client.StackTrace(ctx, godap.StackTraceArguments{ThreadId: t.manager.currentThread(a.ThreadID), Levels: a.Levels})
	if err != nil {
		return "", fmt.Errorf("debug_stack: %w", err)
	}
	return capDebugOutput(formatStack(body.StackFrames)), nil
}

type DebugVarsTool struct{ goDebugTool }

func (t *DebugVarsTool) Description() string {
	return "Return variables for a stack frame, optionally filtered by scope name."
}
func (t *DebugVarsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"frame":               map[string]any{"type": "integer", "description": "Stack frame id"},
		"filter":              map[string]any{"type": "string", "description": "Scope name substring, e.g. Locals"},
		"variables_reference": map[string]any{"type": "integer", "description": "Direct DAP variables reference"},
		"count":               map[string]any{"type": "integer", "description": "Maximum variable count"},
	}}
}
func (t *DebugVarsTool) RequiresApproval(string) bool { return false }
func (t *DebugVarsTool) PreviewCall(argsJSON string) string {
	var a debugVarsArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("debug_vars(frame=%d filter=%s)", a.FrameID, a.Filter)
}
func (t *DebugVarsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugVarsArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("debug_vars: invalid args: %w", err)
	}
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	ref := a.VariablesReference
	if ref == 0 {
		if a.FrameID == 0 {
			return "", fmt.Errorf("debug_vars: frame or variables_reference is required")
		}
		scopes, err := sess.client.Scopes(ctx, godap.ScopesArguments{FrameId: a.FrameID})
		if err != nil {
			return "", fmt.Errorf("debug_vars: scopes: %w", err)
		}
		ref = chooseScope(scopes.Scopes, a.Filter)
		if ref == 0 {
			return "no matching variable scope", nil
		}
	}
	body, err := sess.client.Variables(ctx, godap.VariablesArguments{VariablesReference: ref, Count: a.Count})
	if err != nil {
		return "", fmt.Errorf("debug_vars: %w", err)
	}
	return capDebugOutput(formatVariables(body.Variables)), nil
}

func chooseScope(scopes []godap.Scope, filter string) int {
	filter = strings.ToLower(strings.TrimSpace(filter))
	for _, scope := range scopes {
		if filter == "" || strings.Contains(strings.ToLower(scope.Name), filter) {
			return scope.VariablesReference
		}
	}
	return 0
}

type DebugEvalTool struct{ goDebugTool }

func (t *DebugEvalTool) Description() string {
	return "Evaluate an expression in the active Go debug session."
}
func (t *DebugEvalTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"expression": map[string]any{"type": "string", "description": "Expression to evaluate"},
		"frame":      map[string]any{"type": "integer", "description": "Optional frame id"},
		"context":    map[string]any{"type": "string", "description": "DAP eval context"},
	}, "required": []string{"expression"}}
}
func (t *DebugEvalTool) RequiresApproval(string) bool { return true }
func (t *DebugEvalTool) PreviewCall(argsJSON string) string {
	var a debugEvalArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return "debug_eval(" + a.Expression + ")"
}
func (t *DebugEvalTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a debugEvalArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("debug_eval: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Expression) == "" {
		return "", fmt.Errorf("debug_eval: expression is required")
	}
	sess, err := t.manager.active()
	if err != nil {
		return "", err
	}
	body, err := sess.client.Evaluate(ctx, godap.EvaluateArguments{Expression: a.Expression, FrameId: a.FrameID, Context: a.Context})
	if err != nil {
		return "", fmt.Errorf("debug_eval: %w", err)
	}
	return capDebugOutput(fmt.Sprintf("%s\n%s", body.Type, body.Result)), nil
}

type DebugStopTool struct{ goDebugTool }

func (t *DebugStopTool) Description() string {
	return "Stop the active Go debug session and tear down Delve."
}
func (t *DebugStopTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *DebugStopTool) RequiresApproval(string) bool { return false }
func (t *DebugStopTool) PreviewCall(string) string    { return "debug_stop()" }
func (t *DebugStopTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	return t.manager.stop(ctx)
}

func formatBreakpoints(bps []godap.Breakpoint) string {
	if len(bps) == 0 {
		return "no breakpoints set"
	}
	var out strings.Builder
	for i, bp := range bps {
		fmt.Fprintf(&out, "%d: verified=%t line=%d", i+1, bp.Verified, bp.Line)
		if bp.Message != "" {
			fmt.Fprintf(&out, " message=%s", bp.Message)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func formatStack(frames []godap.StackFrame) string {
	if len(frames) == 0 {
		return "no stack frames"
	}
	var out strings.Builder
	for _, f := range frames {
		path := ""
		if f.Source != nil {
			path = f.Source.Path
		}
		fmt.Fprintf(&out, "#%d %s %s:%d:%d\n", f.Id, f.Name, path, f.Line, f.Column)
	}
	return out.String()
}

func formatVariables(vars []godap.Variable) string {
	if len(vars) == 0 {
		return "no variables"
	}
	var out strings.Builder
	for _, v := range vars {
		line := v.Name + " = " + v.Value
		if v.Type != "" {
			line += " (" + v.Type + ")"
		}
		if v.VariablesReference != 0 {
			line += " ref=" + strconv.Itoa(v.VariablesReference)
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}

func capDebugOutput(s string) string {
	if len(s) <= debugMaxOutputBytes {
		return s
	}
	return s[:debugMaxOutputBytes] + "\n... output truncated ..."
}
