package oneshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
)

// scriptedStreamer is a duplicate of the one in internal/agent — kept here
// because that one is package-private to agent_test.go. We'll dedupe into a
// shared testutil package once enough call sites exist to justify it.
type scriptedStreamer struct {
	turns [][]adapter.StreamEvent
	mu    sync.Mutex
	next  int
}

func (s *scriptedStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.mu.Lock()
	out := make(chan adapter.StreamEvent, 16)
	if s.next >= len(s.turns) {
		s.mu.Unlock()
		close(out)
		return out
	}
	turn := s.turns[s.next]
	s.next++
	s.mu.Unlock()
	go func() {
		defer close(out)
		for _, ev := range turn {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}

func sseToken(s string) adapter.StreamEvent {
	return adapter.StreamEvent{Kind: adapter.EventTokenDelta, Token: s}
}
func sseReason(s string) adapter.StreamEvent {
	return adapter.StreamEvent{Kind: adapter.EventReasoning, Token: s}
}
func sseDone(content string, tcs ...adapter.ToolCall) adapter.StreamEvent {
	msg := &adapter.Message{Role: adapter.RoleAssistant, Content: content, ToolCalls: tcs}
	return adapter.StreamEvent{Kind: adapter.EventDone, Final: msg}
}

// Memory-tool registration parity (memory_save/memory_search carrying the
// embedder + configured strategy) is now tested in
// internal/agentruntime — registerMemoryTools moved there as part of the
// Builder extraction (see agentruntime.Builder.Build).

func TestOneshot_ContentGoesToStdout(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("Hel"), sseToken("lo"), sseDone("Hello")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stdout.String(), "Hello") {
		t.Errorf("stdout missing content: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "Hello") {
		t.Errorf("content leaked to stderr: %q", stderr.String())
	}
}

// TurnDone leaves a "✻ Thought for Ns" footnote on stderr, mirroring
// the TUI footnote so non-interactive runs (CI, scripts) get the same
// quiet receipt of how long the model was busy.
func TestOneshot_EmitsThoughtForFootnoteOnStderr(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("ok"), sseDone("ok")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	var stdout, stderr bytes.Buffer
	if err := stream(context.Background(), cfg, &hist, &stdout, &stderr); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stderr.String(), "› Thought for") {
		t.Errorf("stderr should carry the 'Thought for Ns' footnote: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Thought for") {
		t.Errorf("footnote should not leak to stdout (it'd corrupt redirects): %q", stdout.String())
	}
}

func TestOneshot_ReasoningGoesToStderr(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseReason("thinking"), sseToken("answer"), sseDone("answer")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var stdout, stderr bytes.Buffer
	if err := stream(context.Background(), cfg, &hist, &stdout, &stderr); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stderr.String(), "thinking") {
		t.Errorf("reasoning should be on stderr: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "thinking") {
		t.Errorf("reasoning leaked to stdout: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "answer") {
		t.Errorf("answer missing from stdout: %q", stdout.String())
	}
}

func TestOneshot_JSONStatusReportsSuccessfulRun(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("fixed"), sseDone("fixed")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "fix ticket"}}

	var stdout, stderr bytes.Buffer
	err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true})
	if err != nil {
		t.Fatalf("streamWithOptions: %v", err)
	}
	if stdout.String() != "fixed\n" {
		t.Fatalf("stdout = %q, want final answer only", stdout.String())
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "success" {
		t.Fatalf("status = %q, want success; stderr=%q", got.Status, stderr.String())
	}
	if got.Error != "" {
		t.Fatalf("unexpected json error: %q", got.Error)
	}
	if got.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1", got.Iterations)
	}
}

func TestOneshot_JSONStatusClassifiesApprovalRequired(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeApprovalTool{name: "danger"})
	cfg := agent.LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var stdout, stderr bytes.Buffer
	err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true})
	if err == nil {
		t.Fatalf("expected approval error")
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "approval_required" {
		t.Fatalf("status = %q, want approval_required; stderr=%q", got.Status, stderr.String())
	}
	if got.Error == "" || !strings.Contains(got.Error, "requires approval") {
		t.Fatalf("json error should explain approval failure: %+v", got)
	}
}

func TestOneshot_JSONStatusReportsChangedFilesAndTools(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "list_git_changed_files", ArgsJSON: `{}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeReadTool{name: "list_git_changed_files", result: "src/app.go\nREADME.md"})
	cfg := agent.LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 4}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "inspect changes"}}

	var stdout, stderr bytes.Buffer
	if err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true}); err != nil {
		t.Fatalf("streamWithOptions: %v", err)
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "success" {
		t.Fatalf("status = %q, want success; stderr=%q", got.Status, stderr.String())
	}
	if got.Tools["list_git_changed_files"].Count != 1 {
		t.Fatalf("list_git_changed_files count = %+v, want 1", got.Tools["list_git_changed_files"])
	}
	if len(got.ChangedFiles) != 2 || got.ChangedFiles[0] != "src/app.go" || got.ChangedFiles[1] != "README.md" {
		t.Fatalf("changed files = %#v", got.ChangedFiles)
	}
}

func TestOneshot_JSONStatusClassifiesProviderError(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: errors.New("provider unavailable")}},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var stdout, stderr bytes.Buffer
	err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true})
	if err == nil {
		t.Fatalf("expected provider error")
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "provider_error" {
		t.Fatalf("status = %q, want provider_error; stderr=%q", got.Status, stderr.String())
	}
}

func TestOneshot_JSONStatusClassifiesPolicyDenied(t *testing.T) {
	if got := classifyRunStatus(runOutcome{PolicyDenied: true}); got != "policy_denied" {
		t.Fatalf("status = %q, want policy_denied", got)
	}
}

func TestOneshot_JSONStatusClassifiesTestsFailed(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_tests", ArgsJSON: `{}`})},
		{sseToken("tests failed"), sseDone("tests failed")},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeReadTool{name: "run_tests", result: "$ go test ./...\nexit=1\n--- stdout ---\nFAIL\n"})
	cfg := agent.LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 4}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "test"}}

	var stdout, stderr bytes.Buffer
	if err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true}); err != nil {
		t.Fatalf("streamWithOptions: %v", err)
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "tests_failed" {
		t.Fatalf("status = %q, want tests_failed; stderr=%q", got.Status, stderr.String())
	}
}

func TestOneshot_JSONStatusIgnoresCleanChangedFilesSentinel(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "list_git_changed_files", ArgsJSON: `{}`})},
		{sseToken("clean"), sseDone("clean")},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeReadTool{name: "list_git_changed_files", result: "(no changed files)"})
	cfg := agent.LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 4}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "inspect clean tree"}}

	var stdout, stderr bytes.Buffer
	if err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true}); err != nil {
		t.Fatalf("streamWithOptions: %v", err)
	}

	got := decodeJSONStatus(t, stderr.String())
	if len(got.ChangedFiles) != 0 {
		t.Fatalf("changed files = %#v, want empty", got.ChangedFiles)
	}
}

func TestOneshot_JSONStatusSkipsErroredChangedFilesOutput(t *testing.T) {
	status := RunStatus{}
	changedSeen := map[string]bool{}
	errored := agent.ToolResult{
		ToolName: "list_git_changed_files",
		Output:   "error: list_git_changed_files: git binary not found in PATH",
		Errored:  true,
	}

	recordToolStatus(&status, errored)
	if !errored.Errored {
		collectChangedFiles(&status, changedSeen, errored.Output)
	}

	if len(status.ChangedFiles) != 0 {
		t.Fatalf("changed files = %#v, want empty", status.ChangedFiles)
	}
	if status.Tools["list_git_changed_files"].Errored != 1 {
		t.Fatalf("tool error count = %+v, want 1 errored", status.Tools["list_git_changed_files"])
	}
}

func TestOneshot_RunTestsFailedParsesExitLine(t *testing.T) {
	output := "$ printf 'exit=0' && go test ./...\n--- stdout ---\nexit=0\nFAIL\nexit=1\n--- stderr ---\n"
	if runTestsFailed(output) {
		t.Fatalf("runTestsFailed should ignore non-header exit lines")
	}
	if !runTestsFailed("$ go test ./...\nexit=1\n--- stdout ---\nFAIL\n") {
		t.Fatalf("runTestsFailed should use the actual run_tests exit line")
	}
}

func TestOneshot_JSONStatusClassifiesBlockedNeedsClarification(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("BLOCKED: needs clarification from the ticket reporter"), sseDone("BLOCKED: needs clarification from the ticket reporter")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "fix vague ticket"}}

	var stdout, stderr bytes.Buffer
	if err := streamWithOptions(context.Background(), cfg, &hist, &stdout, &stderr, StreamOptions{JSONStatus: true}); err != nil {
		t.Fatalf("streamWithOptions: %v", err)
	}

	got := decodeJSONStatus(t, stderr.String())
	if got.Status != "blocked_needs_clarification" {
		t.Fatalf("status = %q, want blocked_needs_clarification; stderr=%q", got.Status, stderr.String())
	}
}

func decodeJSONStatus(t *testing.T, output string) RunStatus {
	t.Helper()
	marker := "\n{\n  \"status\""
	idx := strings.LastIndex(output, marker)
	if idx < 0 {
		t.Fatalf("stderr missing JSON status object: %q", output)
	}
	var got RunStatus
	if err := json.Unmarshal([]byte(output[idx+1:]), &got); err != nil {
		t.Fatalf("decode JSON status: %v\n%s", err, output[idx+1:])
	}
	return got
}

func TestOneshot_ApprovalNeededWithoutBypassErrors(t *testing.T) {
	// A tool call that requires approval, with BypassPermissions=false:
	// the loop emits ApprovalNeeded; oneshot must surface this as an
	// error (no human in the loop) rather than block.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeApprovalTool{name: "danger"})
	cfg := agent.LoopConfig{
		Adapter: streamer, Registry: reg,
		BypassPermissions: false,
		MaxIterations:     3,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error when ApprovalNeeded fires without --yolo")
	}
	if !strings.Contains(err.Error(), "approval") {
		t.Errorf("error should mention approval; got %v", err)
	}
}

func TestOneshot_PropagatesAdapterError(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: errors.New("boom")}},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include underlying message; got %v", err)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr should also surface the error: %q", stderr.String())
	}
}

// composeSystemPrompt's xAI framing and preflight's error formatting are
// now tested in internal/agentruntime — both moved there as part of the
// Builder extraction.

func TestOneshot_StreamRecoversTurnPanic(t *testing.T) {
	cfg := agent.LoopConfig{Adapter: panicStreamer{}, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}
	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "agent turn panicked") {
		t.Fatalf("expected panic error, got %v", err)
	}
}

type panicStreamer struct{}

func (panicStreamer) ChatStream(context.Context, []adapter.Message, []adapter.Tool) <-chan adapter.StreamEvent {
	panic("boom")
}

// fakeApprovalTool: a Tool that always requires approval but is never executed
// in these tests (the loop never reaches Execute when ApprovalNeeded is denied).
type fakeApprovalTool struct{ name string }

func (m *fakeApprovalTool) Name() string                 { return m.name }
func (m *fakeApprovalTool) Description() string          { return "fake" }
func (m *fakeApprovalTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (m *fakeApprovalTool) RequiresApproval(string) bool { return true }
func (m *fakeApprovalTool) PreviewCall(string) string    { return m.name + "()" }
func (m *fakeApprovalTool) Execute(_ context.Context, _ string) (string, error) {
	return "should not run", nil
}

// fakeReadTool is an approval-free tool used to prove JSON status captures
// tool execution counts and line-oriented changed-file output.
type fakeReadTool struct {
	name   string
	result string
}

func (m *fakeReadTool) Name() string                 { return m.name }
func (m *fakeReadTool) Description() string          { return "fake read" }
func (m *fakeReadTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (m *fakeReadTool) RequiresApproval(string) bool { return false }
func (m *fakeReadTool) PreviewCall(string) string    { return m.name + "()" }
func (m *fakeReadTool) Execute(_ context.Context, _ string) (string, error) {
	return m.result, nil
}

// Router-resolver mode-gating (routerModelResolver/routerResolve) is now
// tested in internal/agentruntime — moved there as part of the Builder
// extraction.
