package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// interruptibleStreamer emits `pre` immediately, then blocks until either
// `hold` is closed or ctx is cancelled, then emits `post`. Used to time a
// cancel precisely between two streaming events.
type interruptibleStreamer struct {
	pre  []adapter.StreamEvent
	hold chan struct{}
	post []adapter.StreamEvent
}

func (s *interruptibleStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 16)
	go func() {
		defer close(out)
		for _, ev := range s.pre {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.hold:
		}
		for _, ev := range s.post {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}

// waiterTool signals on `started` when its Execute begins, then blocks
// until ctx is cancelled. Returning (out="", err=ctx.Err()) drops the
// loop deterministically into the cancel path — no select-race on
// send. The Tool-level cancel propagation in executeToolCall translates
// the ctx.Err() return into a synthesized interrupt marker via the
// caller's cancel branch.
//
// parallelSafe toggles ParallelSafe so the same struct can drive both
// the serial cancel test (one in-flight tool, queued behind it) and
// the parallel cancel test (one blocker mixed with fast workers in a
// single batched executeToolCallsParallel call).
type waiterTool struct {
	name         string
	started      chan struct{}
	parallelSafe bool
}

func (w *waiterTool) Name() string                 { return w.name }
func (w *waiterTool) Description() string          { return "test " + w.name }
func (w *waiterTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (w *waiterTool) RequiresApproval(string) bool { return false }
func (w *waiterTool) ParallelSafe(string) bool     { return w.parallelSafe }
func (w *waiterTool) PreviewCall(string) string    { return w.name + "()" }
func (w *waiterTool) Execute(ctx context.Context, _ string) (string, error) {
	close(w.started)
	<-ctx.Done()
	return "", ctx.Err()
}

// drainInterruptTurn runs Turn against a ctx the test can cancel, and
// returns the collected events along with Turn's terminal error. Unlike
// runTurnSync it does not answer ApprovalNeeded events (the interrupt
// tests don't exercise approval).
func drainInterruptTurn(
	t *testing.T,
	ctx context.Context,
	cfg LoopConfig,
	history *[]adapter.Message,
	onEvent func(Event, context.CancelFunc),
	cancel context.CancelFunc,
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
		if onEvent != nil {
			onEvent(ev, cancel)
		}
	}
	return collected, <-errCh
}

// TestInterrupt_MidStream_PreservesPartialAssistant covers the most
// common case: user hits Enter while content tokens are still streaming.
// The agent loop must preserve whatever was streamed as a content-only
// assistant message in history (no orphan tool_use blocks), and emit
// TurnInterrupted carrying the partial text so the UI can render the
// cancel cleanly. Without this, the follow-up turn would replay a
// truncated conversation and the partial response would be lost.
func TestInterrupt_MidStream_PreservesPartialAssistant(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	streamer := &interruptibleStreamer{
		pre:  []adapter.StreamEvent{sseToken("Hello, w")},
		hold: hold,
		post: []adapter.StreamEvent{sseDone("Hello, world")},
	}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := drainInterruptTurn(t, ctx, cfg, &hist, func(ev Event, c context.CancelFunc) {
		// Cancel the moment the first content token surfaces — that
		// way the test exercises the partial-preserve path
		// deterministically rather than racing the streamer.
		if _, ok := ev.(ContentToken); ok {
			c()
		}
	}, cancel)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (user + partial assistant); hist=%+v", len(hist), hist)
	}
	if hist[1].Role != adapter.RoleAssistant {
		t.Fatalf("history[1].Role = %q, want assistant", hist[1].Role)
	}
	if hist[1].Content != "Hello, w" {
		t.Fatalf("history[1].Content = %q, want %q", hist[1].Content, "Hello, w")
	}
	if len(hist[1].ToolCalls) != 0 {
		t.Errorf("partial assistant message must not carry tool_calls; got %+v", hist[1].ToolCalls)
	}

	var ti *TurnInterrupted
	for i := range events {
		if e, ok := events[i].(TurnInterrupted); ok {
			ti = &e
		}
	}
	if ti == nil {
		t.Fatalf("expected TurnInterrupted event; got %+v", events)
	}
	if ti.PartialContent != "Hello, w" {
		t.Errorf("TurnInterrupted.PartialContent = %q, want %q", ti.PartialContent, "Hello, w")
	}
	if ti.OrphanedCalls != 0 {
		t.Errorf("TurnInterrupted.OrphanedCalls = %d, want 0 (no tool batch in flight)", ti.OrphanedCalls)
	}
	if hasEvent[ErrorEvent](events) {
		t.Errorf("user-initiated cancel must not emit ErrorEvent (would render as red ✗)")
	}
}

// TestInterrupt_MidStream_NoTokensPreservesNothing covers the edge
// where the cancel lands before any token streamed. History should
// stay at just the user message — no empty assistant entry — and the
// TurnInterrupted carries an empty PartialContent. Without the
// partialAssistantMessage nil-guard this case would append a
// content="" assistant row that providers reject on the next turn.
func TestInterrupt_MidStream_NoTokensPreservesNothing(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	streamer := &interruptibleStreamer{
		pre:  nil,
		hold: hold,
		post: []adapter.StreamEvent{sseDone("never sent")},
	}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel from a goroutine after a brief moment so the streamer is
	// blocked on hold when ctx fires.
	var once sync.Once
	events, err := drainInterruptTurn(t, ctx, cfg, &hist, func(ev Event, c context.CancelFunc) {
		// First IterationStart confirms Turn entered the loop; cancel
		// from there so the streamer is parked on hold.
		if _, ok := ev.(IterationStart); ok {
			once.Do(c)
		}
	}, cancel)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1 (user only, no empty assistant); hist=%+v", len(hist), hist)
	}
	var ti *TurnInterrupted
	for i := range events {
		if e, ok := events[i].(TurnInterrupted); ok {
			ti = &e
		}
	}
	if ti == nil {
		t.Fatalf("expected TurnInterrupted event")
	}
	if ti.PartialContent != "" {
		t.Errorf("TurnInterrupted.PartialContent = %q, want empty", ti.PartialContent)
	}
}

// TestInterrupt_MidSerialToolBatch_SynthesizesOrphanResults covers the
// hairier case: the model emitted multiple tool_use blocks, the first
// runs to completion, the second gets cancelled mid-flight, and the
// third never executes. History must end with a tool_result for every
// tool_use — providers reject the alternative — and orphans get the
// "interrupted by user" synthetic content. The completed tool keeps
// its real result; only the orphans are synthetic.
func TestInterrupt_MidSerialToolBatch_SynthesizesOrphanResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("",
			adapter.ToolCall{ID: "c1", Name: "fast", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "c2", Name: "waiter", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "c3", Name: "queued", ArgsJSON: `{}`},
		)},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "fast", output: "real-A"})
	waiterStarted := make(chan struct{})
	reg.Register(&waiterTool{name: "waiter", started: waiterStarted})
	reg.Register(&mockTool{name: "queued", output: "real-C-never-runs"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	// Cancel only after the waiter has signalled it started — that's
	// when c1's real result has already been appended and c2 is
	// genuinely in flight. Cancel from a goroutine so the consumer
	// loop in drainInterruptTurn keeps draining events.
	go func() {
		<-waiterStarted
		cancel()
	}()

	events, err := drainInterruptTurn(t, ctx, cfg, &hist, nil, cancel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Expected history: user, assistant(3 tool_calls), tool(c1 real),
	// tool(c2 synth), tool(c3 synth). One tool_result per tool_use ID,
	// c2 + c3 both synthetic since the cancel landed during c2's
	// execution.
	if got, want := len(hist), 5; got != want {
		t.Fatalf("history len = %d, want %d; hist=%+v", got, want, hist)
	}
	if hist[1].Role != adapter.RoleAssistant || len(hist[1].ToolCalls) != 3 {
		t.Fatalf("hist[1] should be assistant with 3 tool_calls; got %+v", hist[1])
	}
	gotIDs := map[string]string{}
	for _, m := range hist[2:] {
		if m.Role != adapter.RoleTool {
			t.Fatalf("expected tool roles for hist[2:], got %+v", m)
		}
		gotIDs[m.ToolCallID] = m.Content
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if _, ok := gotIDs[want]; !ok {
			t.Errorf("missing tool_result for tool_use %s; gotIDs=%+v", want, gotIDs)
		}
	}
	if gotIDs["c1"] != "real-A" {
		t.Errorf("c1 (completed before cancel) should keep its real result; got %q", gotIDs["c1"])
	}
	if gotIDs["c2"] != interruptedToolResult {
		t.Errorf("c2 (cancelled mid-execute) should carry synthetic interrupt marker; got %q", gotIDs["c2"])
	}
	if gotIDs["c3"] != interruptedToolResult {
		t.Errorf("c3 (never executed) should carry synthetic interrupt marker; got %q", gotIDs["c3"])
	}

	var ti *TurnInterrupted
	for i := range events {
		if e, ok := events[i].(TurnInterrupted); ok {
			ti = &e
		}
	}
	if ti == nil {
		t.Fatalf("expected TurnInterrupted event; got events=%+v", events)
	}
	if ti.OrphanedCalls != 2 {
		t.Errorf("TurnInterrupted.OrphanedCalls = %d, want 2 (c2 + c3)", ti.OrphanedCalls)
	}
}

// ctxBlockerTool blocks indefinitely on ctx until cancelled, then
// returns ctx.Err(). Unlike waiterTool it doesn't expose a "started"
// signal because the parallel cancel test gates on event delivery
// (ToolResult fan-in) instead of tool-side timing — see
// TestInterrupt_MidParallelToolBatch for the reasoning.
type ctxBlockerTool struct {
	name         string
	parallelSafe bool
}

func (b *ctxBlockerTool) Name() string                 { return b.name }
func (b *ctxBlockerTool) Description() string          { return "test " + b.name }
func (b *ctxBlockerTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (b *ctxBlockerTool) RequiresApproval(string) bool { return false }
func (b *ctxBlockerTool) ParallelSafe(string) bool     { return b.parallelSafe }
func (b *ctxBlockerTool) PreviewCall(string) string    { return b.name + "()" }
func (b *ctxBlockerTool) Execute(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestInterrupt_MidParallelToolBatch_KeepsCompletedSynthesizesBlocker
// exercises the parallel-batch cancel path (separate code from the
// serial path: executeToolCallsParallel + appendToolResultsWithInterrupts).
// Three parallel-safe tools run concurrently: two return immediately,
// one blocks on ctx until cancelled. After the cancel, workers that
// completed cleanly must keep their real result; only the cancelled
// blocker gets the synthetic interrupt marker. The invariant we're
// protecting is mixed-state batches — without per-entry decisioning we
// would either lose the fast results (returning nil, err from the
// parallel helper) or synthesize every slot (uniform interrupt marker
// for the whole batch, wasting two completed tool calls).
//
// Timing on CI: the cancel is gated on event delivery (ToolResult
// fan-in for both fast tools), NOT on a tool-side started signal.
// Reason: with parallel goroutines + single-CPU CI scheduling, a
// tool-side "started" trigger fires when the blocker enters Execute,
// but the fast tools' goroutines may not yet have entered their
// executeToolCall — cancelling there causes their first send (which
// happens before Execute) to return ctx.Err() and the test sees
// synthetic results for everyone. Waiting until both fast ToolResults
// have been received by the consumer proves their full executeToolCall
// path (including post-Execute send) ran successfully and the cancel
// can no longer affect them.
func TestInterrupt_MidParallelToolBatch_KeepsCompletedSynthesizesBlocker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("",
			adapter.ToolCall{ID: "p1", Name: "fast_a", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "p2", Name: "fast_b", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "p3", Name: "blocker", ArgsJSON: `{}`},
		)},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "fast_a", parallelSafe: true, output: "A"})
	reg.Register(&mockTool{name: "fast_b", parallelSafe: true, output: "B"})
	reg.Register(&ctxBlockerTool{name: "blocker", parallelSafe: true})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "fan out"}}

	var gotA, gotB bool
	events, err := drainInterruptTurn(t, ctx, cfg, &hist, func(ev Event, c context.CancelFunc) {
		// Cancel only after BOTH fast tools' ToolResult events have
		// surfaced — meaning their executeToolCall path has fully
		// returned and their real result is in the parallel results
		// slice. Blocker may or may not have entered its Execute yet
		// (depends on scheduler); either way it lands in the synthetic
		// branch because its err is ctx.Err().
		tr, ok := ev.(ToolResult)
		if !ok || tr.Errored {
			return
		}
		switch tr.ToolName {
		case "fast_a":
			gotA = true
		case "fast_b":
			gotB = true
		}
		if gotA && gotB {
			c()
		}
	}, cancel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if got, want := len(hist), 5; got != want {
		t.Fatalf("history len = %d, want %d; hist=%+v", got, want, hist)
	}
	gotByID := map[string]string{}
	for _, m := range hist[2:] {
		if m.Role != adapter.RoleTool {
			t.Fatalf("expected tool roles after assistant; got %+v", m)
		}
		gotByID[m.ToolCallID] = m.Content
	}
	if gotByID["p1"] != "A" {
		t.Errorf("p1 completed before cancel; want real result %q, got %q", "A", gotByID["p1"])
	}
	if gotByID["p2"] != "B" {
		t.Errorf("p2 completed before cancel; want real result %q, got %q", "B", gotByID["p2"])
	}
	if gotByID["p3"] != interruptedToolResult {
		t.Errorf("p3 (blocker, cancelled mid-execute) must carry synthetic marker; got %q", gotByID["p3"])
	}

	var ti *TurnInterrupted
	for i := range events {
		if e, ok := events[i].(TurnInterrupted); ok {
			ti = &e
		}
	}
	if ti == nil {
		t.Fatalf("expected TurnInterrupted event; got events=%+v", events)
	}
	if ti.OrphanedCalls != 1 {
		t.Errorf("TurnInterrupted.OrphanedCalls = %d, want 1 (only blocker; fast workers kept real results)", ti.OrphanedCalls)
	}
	if hasEvent[ErrorEvent](events) {
		t.Errorf("user-initiated cancel must not emit ErrorEvent")
	}
}

// TestInterrupt_DuringForegroundSubagent_PreservesParentHistory
// covers the most important subagent interaction with the interrupt
// flow: a long-running foreground tool (which a foreground Agent call
// effectively is — the parent's loop is blocked on tool.Execute while
// the child runs its own Turn) gets cancelled mid-flight, the parent
// must synthesize a tool_result for the orphaned tool_use, emit
// TurnInterrupted (NOT ErrorEvent), and leave history in a state the
// next turn can use without provider rejection. We use a stub tool
// here rather than the real Agent tool because the invariants under
// test are purely on the parent loop side — the parent doesn't care
// whether the long-running tool is a child Turn or any other
// ctx-respecting blocking operation. The real Agent tool's child-side
// behavior (ctx propagation from parent, transcript persistence) is
// covered by agent_tool_test.go.
func TestInterrupt_DuringForegroundSubagent_PreservesParentHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "sub-1", Name: "subagent", ArgsJSON: `{"type":"Explore","prompt":"map the auth layer"}`})},
	}}
	reg := NewRegistry()
	subagentStarted := make(chan struct{})
	// Stand-in for the real Agent tool: blocks on ctx, returns
	// ctx.Err() when cancelled. The interesting invariants
	// (synthetic tool_result, TurnInterrupted, no ErrorEvent) flow
	// from "tool returned ctx.Err()", not from any subagent-specific
	// machinery.
	reg.Register(&waiterTool{name: "subagent", started: subagentStarted})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "spawn a subagent"}}

	go func() {
		<-subagentStarted
		cancel()
	}()

	events, err := drainInterruptTurn(t, ctx, cfg, &hist, nil, cancel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// History: user, assistant(1 tool_call), tool(synthetic).
	if got, want := len(hist), 3; got != want {
		t.Fatalf("history len = %d, want %d; hist=%+v", got, want, hist)
	}
	if hist[1].Role != adapter.RoleAssistant || len(hist[1].ToolCalls) != 1 {
		t.Fatalf("hist[1] should be assistant with 1 tool_call; got %+v", hist[1])
	}
	if hist[1].ToolCalls[0].ID != "sub-1" {
		t.Errorf("tool_use id should be preserved; got %q", hist[1].ToolCalls[0].ID)
	}
	if hist[2].Role != adapter.RoleTool || hist[2].ToolCallID != "sub-1" {
		t.Fatalf("hist[2] should be tool_result for sub-1; got %+v", hist[2])
	}
	if hist[2].Content != interruptedToolResult {
		t.Errorf("subagent tool_result on cancel must be synthetic %q; got %q", interruptedToolResult, hist[2].Content)
	}

	var ti *TurnInterrupted
	for i := range events {
		if e, ok := events[i].(TurnInterrupted); ok {
			ti = &e
		}
	}
	if ti == nil {
		t.Fatalf("expected TurnInterrupted; got events=%+v", events)
	}
	if ti.OrphanedCalls != 1 {
		t.Errorf("TurnInterrupted.OrphanedCalls = %d, want 1 (the subagent tool_use)", ti.OrphanedCalls)
	}
	if hasEvent[ErrorEvent](events) {
		t.Errorf("interrupting a foreground subagent must not surface as an ErrorEvent — it's a cancel, not a failure")
	}
}

// TestInterrupt_AdapterErrorStillEmitsErrorEvent guards the
// isCancelErr branch from accidentally swallowing real adapter
// failures. A non-cancel error must continue to flow through
// ErrorEvent (red ✗ in the TUI) and Turn must return the original
// error, not context.Canceled.
func TestInterrupt_AdapterErrorStillEmitsErrorEvent(t *testing.T) {
	boom := errors.New("upstream 500")
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: boom}},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("expected upstream error to propagate; got %v", err)
	}
	if !hasEvent[ErrorEvent](events) {
		t.Errorf("non-cancel error should still emit ErrorEvent")
	}
	if hasEvent[TurnInterrupted](events) {
		t.Errorf("non-cancel error must NOT emit TurnInterrupted")
	}
}
