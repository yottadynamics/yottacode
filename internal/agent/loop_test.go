package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/permissions"
)

// --- test doubles ---------------------------------------------------------

// scriptedStreamer returns a pre-baked sequence of StreamEvents for each
// successive call to ChatStream. The Nth call yields the Nth slot from
// `turns`. Once turns are exhausted, further calls produce an empty stream.
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

// mockTool is the minimum implementation that satisfies Tool. Tests set
// fields per case to express "this tool needs approval" or "this tool
// errors on execute".
type mockTool struct {
	name             string
	requiresApproval bool
	parallelSafe     bool
	output           string
	execErr          error
	delay            time.Duration
}

func (m *mockTool) Name() string                 { return m.name }
func (m *mockTool) Description() string          { return "test " + m.name }
func (m *mockTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (m *mockTool) RequiresApproval(string) bool { return m.requiresApproval }
func (m *mockTool) ParallelSafe(string) bool     { return m.parallelSafe }
func (m *mockTool) PreviewCall(string) string    { return m.name + "()" }
func (m *mockTool) Execute(_ context.Context, _ string) (string, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.output, m.execErr
}

// runTurnSync runs Turn in a goroutine, drains the events channel, and
// answers any ApprovalNeeded events via decide. Returns the collected events
// plus the turn's terminal error (if any).
func runTurnSync(
	t *testing.T,
	ctx context.Context,
	cfg LoopConfig,
	history *[]adapter.Message,
	decide func(ApprovalNeeded) Decision,
) ([]Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	decisions := make(chan Decision, 1)
	errCh := make(chan error, 1)

	go func() {
		err := Turn(ctx, cfg, history, events, decisions)
		close(events)
		errCh <- err
	}()

	var collected []Event
	for ev := range events {
		collected = append(collected, ev)
		if a, ok := ev.(ApprovalNeeded); ok {
			if decide == nil {
				t.Fatalf("ApprovalNeeded fired but no decide func provided")
			}
			decisions <- decide(a)
		}
	}
	return collected, <-errCh
}

// SSE shorthand helpers
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

func sseDoneWithStopReason(content, stopReason string, tcs ...adapter.ToolCall) adapter.StreamEvent {
	msg := &adapter.Message{
		Role:       adapter.RoleAssistant,
		Content:    content,
		ToolCalls:  tcs,
		StopReason: stopReason,
	}
	return adapter.StreamEvent{Kind: adapter.EventDone, Final: msg}
}

// hasEvent returns true if events contains an entry of T.
func hasEvent[T Event](events []Event) bool {
	for _, e := range events {
		if _, ok := e.(T); ok {
			return true
		}
	}
	return false
}

// --- tests -----------------------------------------------------------------

func TestLoop_SimpleChat(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("Hel"), sseToken("lo"), sseDone("Hello")},
	}}
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      NewRegistry(),
		MaxIterations: 3,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !hasEvent[ContentToken](events) {
		t.Errorf("expected ContentToken events; got %+v", events)
	}
	if !hasEvent[AssistantMessage](events) {
		t.Errorf("expected AssistantMessage event")
	}
	if !hasEvent[IterationStart](events) {
		t.Errorf("expected IterationStart event")
	}
	if !hasEvent[TurnDone](events) {
		t.Errorf("expected TurnDone event")
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("plain chat should not request approval")
	}
}

func TestLoop_ReasoningTokensFlowSeparately(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseReason("thinking"), sseToken("answer"), sseDone("answer")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "?"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var sawReason, sawContent bool
	var reasonIdx, contentIdx int
	for i, e := range events {
		switch e.(type) {
		case ReasoningToken:
			sawReason = true
			reasonIdx = i
		case ContentToken:
			if !sawContent {
				contentIdx = i
				sawContent = true
			}
		}
	}
	if !sawReason || !sawContent {
		t.Fatalf("expected both reasoning and content events; got %+v", events)
	}
	if reasonIdx >= contentIdx {
		t.Errorf("reasoning should precede content; got reason@%d, content@%d", reasonIdx, contentIdx)
	}
}

func TestLoop_ToolCallNoApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		// turn 1: model calls the tool
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "lookup", ArgsJSON: `{}`})},
		// turn 2: model produces final answer
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "lookup", output: "value"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "look"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("tool with RequiresApproval=false should not prompt")
	}
	if !hasEvent[ToolStart](events) {
		t.Errorf("expected ToolStart")
	}
	if !hasEvent[IterationContinue](events) {
		t.Errorf("expected IterationContinue for tool calls")
	}
	if !hasEvent[ToolResult](events) {
		t.Errorf("expected ToolResult")
	}
	if !hasEvent[TurnDone](events) {
		t.Errorf("expected TurnDone after second turn")
	}
	// History should now have user + assistant(toolcall) + tool(result) + assistant(final).
	if len(hist) != 4 {
		t.Errorf("history len = %d, want 4 (user, assistant+toolcall, tool, assistant)", len(hist))
	}
}

func TestLoop_BypassPermissionsAutoApproves(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hi"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, BypassPermissions: true, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("--dangerously-skip-permissions should suppress approval prompt")
	}
	var auto *ApprovalAuto
	for i := range events {
		if a, ok := events[i].(ApprovalAuto); ok {
			auto = &a
		}
	}
	if auto == nil || auto.Source != "bypass-permissions" {
		t.Errorf("expected ApprovalAuto with Source=bypass-permissions; got %+v", auto)
	}
}

// permsForTest builds a project-local permissions store under tmpdir
// and seeds the file with an initial JSON shape. Returns the loaded
// store + the tmpdir (used as Cwd in LoopConfig).
func permsForTest(t *testing.T, allow, ask, deny []string) (*permissions.Permissions, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shape := map[string]map[string][]string{
		"permissions": {"allow": allow, "ask": ask, "deny": deny},
	}
	b, _ := json.MarshalIndent(shape, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".yottacode", "permissions.json"), b, 0o644); err != nil {
		t.Fatalf("write permissions.json: %v", err)
	}
	p, err := permissions.Load(dir)
	if err != nil {
		t.Fatalf("permissions.Load: %v", err)
	}
	return p, dir
}

func TestLoop_PermissionsAllowAutoApproves(t *testing.T) {
	perms, cwd := permsForTest(t, []string{"Bash(echo *)"}, nil, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hello"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "x"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("matching allow rule should suppress prompt")
	}
	var auto *ApprovalAuto
	for i := range events {
		if a, ok := events[i].(ApprovalAuto); ok {
			auto = &a
		}
	}
	if auto == nil || auto.Source != "permissions" {
		t.Errorf("expected ApprovalAuto with Source=permissions; got %+v", auto)
	}
}

func TestLoop_PermissionsDenyBlocksEvenUnderBypass(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Bash(rm *)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /tmp/x"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		BypassPermissions: true, // even bypass must respect deny
		Cwd:               NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if hasEvent[ToolStart](events) {
		t.Errorf("deny rule should prevent execution even with --dangerously-skip-permissions")
	}
	var auto *ApprovalAuto
	for i := range events {
		if a, ok := events[i].(ApprovalAuto); ok {
			auto = &a
		}
	}
	if auto == nil || auto.Source != "deny-rule" {
		t.Errorf("expected ApprovalAuto with Source=deny-rule; got %+v", auto)
	}
}

func TestLoop_PermissionsAskForcesPromptOnReadOnlyTool(t *testing.T) {
	// Read tool wouldn't normally prompt — Ask rule promotes it to one.
	perms, cwd := permsForTest(t, nil, []string{"Read(.env)"}, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "read_file", ArgsJSON: `{"path":".env"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "read_file", requiresApproval: false, output: "secret"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(_ ApprovalNeeded) Decision {
		return AllowOnce
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Errorf("Ask rule should force a prompt even on a read-only tool")
	}
}

func TestLoop_ApprovalGrantedRunsTool(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "mut", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "mut", requiresApproval: true, output: "executed"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(_ ApprovalNeeded) Decision {
		return AllowOnce
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Errorf("expected ApprovalNeeded")
	}
	if !hasEvent[ToolStart](events) {
		t.Errorf("ToolStart should fire after AllowOnce")
	}
	if !hasEvent[ToolResult](events) {
		t.Errorf("ToolResult should fire after AllowOnce")
	}
}

func TestLoop_ApprovalAllowAlwaysAppendsToPermissionsLocal(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"go test ./..."}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "x"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, _ = runTurnSync(t, context.Background(), cfg, &hist, func(_ ApprovalNeeded) Decision {
		return AllowAlways
	})

	// Should have written "Bash(go *)" to permissions.local.json.
	b, err := os.ReadFile(filepath.Join(cwd, ".yottacode", "permissions.local.json"))
	if err != nil {
		t.Fatalf("permissions.local.json was not written: %v", err)
	}
	if !strings.Contains(string(b), "Bash(go *)") {
		t.Errorf("expected derived rule Bash(go *) in permissions.local.json; got: %s", b)
	}
}

func TestLoop_DeniedToolReportsToModel(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "mut", ArgsJSON: `{}`})},
		{sseToken("oh well"), sseDone("oh well")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "mut", requiresApproval: true, output: "shouldnotrun"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, err := runTurnSync(t, context.Background(), cfg, &hist, func(_ ApprovalNeeded) Decision {
		return Deny
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// History should include a tool message with "denied by user".
	var sawDenied bool
	for _, m := range hist {
		if m.Role == adapter.RoleTool && m.Content == "denied by user" {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Errorf("expected tool result 'denied by user' in history; got %+v", hist)
	}
}

func TestLoop_HitsIterCap(t *testing.T) {
	// Every turn calls the tool again; the loop must bail out at MaxIterations.
	tc := []adapter.StreamEvent{sseDone("", adapter.ToolCall{ID: "x", Name: "spinner", ArgsJSON: `{}`})}
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{tc, tc, tc, tc, tc, tc}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "spinner", output: "again"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "loop"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !hasEvent[IterCap](events) {
		t.Errorf("expected IterCap event when MaxIterations exceeded")
	}
}

func TestLoop_FallbackEventPlumbedThroughToAgentEvent(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{
			{
				Kind:           adapter.EventFallback,
				FallbackFrom:   "anthropic/claude-haiku-4-5",
				FallbackTo:     "openai/gpt-4o",
				FallbackReason: "rate limited",
				FallbackPolicy: "fallback-chain",
			},
			sseToken("hello"),
			sseDone("hello"),
		},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	var fb *Fallback
	for i := range events {
		if f, ok := events[i].(Fallback); ok {
			fb = &f
			break
		}
	}
	if fb == nil {
		t.Fatalf("expected Fallback event in stream, got: %+v", events)
	}
	if fb.From != "anthropic/claude-haiku-4-5" || fb.To != "openai/gpt-4o" {
		t.Errorf("Fallback labels = %q→%q, want anthropic/claude-haiku-4-5→openai/gpt-4o", fb.From, fb.To)
	}
	if fb.Reason != "rate limited" {
		t.Errorf("Fallback reason = %q, want %q", fb.Reason, "rate limited")
	}
	if fb.Policy != "fallback-chain" {
		t.Errorf("Fallback policy = %q, want fallback-chain", fb.Policy)
	}
}

func TestLoop_AdapterErrorPropagates(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: errors.New("boom")}},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err == nil {
		t.Fatalf("expected error from Turn")
	}
	if !hasEvent[ErrorEvent](events) {
		t.Errorf("expected ErrorEvent")
	}
}

func TestLoop_ParallelSafeReadOnlyToolsRunConcurrently(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("",
			adapter.ToolCall{ID: "c1", Name: "read_a", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "c2", Name: "read_b", ArgsJSON: `{}`},
		)},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "read_a", parallelSafe: true, output: "A", delay: 120 * time.Millisecond})
	reg.Register(&mockTool{name: "read_b", parallelSafe: true, output: "B", delay: 120 * time.Millisecond})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "read"}}

	start := time.Now()
	_, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 220*time.Millisecond {
		t.Fatalf("parallel-safe tools ran too slowly, elapsed=%v", elapsed)
	}
}

func TestLoop_TruncatedOutputAutoContinues(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("part"), sseDoneWithStopReason("part", "length")},
		{sseToken(" two"), sseDone("part two")},
	}}
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      NewRegistry(),
		MaxIterations: 3,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "tell me"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var sawContinue bool
	for _, ev := range events {
		if c, ok := ev.(IterationContinue); ok && c.Reason == continueReasonTruncatedOutput {
			sawContinue = true
		}
	}
	if !sawContinue {
		t.Fatalf("expected IterationContinue with truncated_output; got %+v", events)
	}
	if len(hist) != 4 {
		t.Fatalf("history len = %d, want 4", len(hist))
	}
	if hist[2].Role != adapter.RoleUser || hist[2].Content == "" {
		t.Fatalf("expected injected continuation user message, got %+v", hist[2])
	}
	if got := hist[len(hist)-1].Content; got != "part two" {
		t.Fatalf("final content = %q, want %q", got, "part two")
	}
}

// TestLoop_PlanAwareToolEmitsTodoUpdate verifies the integration
// between TodoWriteTool and the loop: after a planAware tool runs,
// the loop should snapshot its store and emit a TodoUpdate event
// carrying the new list.
func TestLoop_PlanAwareToolEmitsTodoUpdate(t *testing.T) {
	args := `{"todos":[{"content":"do thing","status":"in_progress"}]}`
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "todo_write", ArgsJSON: args})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&TodoWriteTool{Store: NewPlanStore()})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "plan"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var update *TodoUpdate
	for _, ev := range events {
		if u, ok := ev.(TodoUpdate); ok {
			update = &u
		}
	}
	if update == nil {
		t.Fatalf("expected TodoUpdate event; got %+v", events)
	}
	if len(update.Todos) != 1 {
		t.Fatalf("TodoUpdate.Todos len = %d, want 1", len(update.Todos))
	}
	if update.Todos[0].Status != TodoInProgress || update.Todos[0].Content != "do thing" {
		t.Errorf("TodoUpdate carries wrong item: %+v", update.Todos[0])
	}
}

// TestLoop_NonPlanToolDoesNotEmitTodoUpdate guards the planAware
// branch from accidentally firing for ordinary tools (regression
// guard: a future refactor that drops the interface check would
// break this test, not just leave it silently broken).
func TestLoop_NonPlanToolDoesNotEmitTodoUpdate(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "noop", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "noop", output: "did nothing"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[TodoUpdate](events) {
		t.Errorf("non-plan tool should not trigger TodoUpdate")
	}
}
