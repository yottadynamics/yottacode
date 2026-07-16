package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestLoopControl_HiddenOutsideLoopTurn(t *testing.T) {
	// Not a loop turn: filter hides loop_control, and a stray call refuses.
	if iterationToolFilter(false, false)("loop_control") {
		t.Error("loop_control should be hidden when no loop turn is active")
	}
	tool := &LoopControlTool{State: &LoopControlState{}}
	out, err := tool.Execute(context.Background(), `{"action":"stop"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to stop") {
		t.Errorf("stray stop should refuse cleanly, got %q", out)
	}
	if s := tool.State; s.IsActive() {
		t.Error("Execute must not mark a non-loop turn active")
	}
	if stop, _ := tool.State.ConsumeStop(); stop {
		t.Error("a refused call must not set the stop flag")
	}
}

func TestLoopControl_StopDuringLoopTurn(t *testing.T) {
	if !iterationToolFilter(false, true)("loop_control") {
		t.Error("loop_control should be advertised during a loop turn")
	}
	// Plan-mode gating still holds through the composed filter.
	if iterationToolFilter(false, true)("exit_plan_mode") {
		t.Error("exit_plan_mode should still be hidden outside plan mode")
	}
	if !iterationToolFilter(false, true)("read_file") {
		t.Error("ordinary tools should always pass")
	}

	st := &LoopControlState{}
	st.SetTurnActive(true)
	tool := &LoopControlTool{State: st}
	out, err := tool.Execute(context.Background(), `{"action":"stop","reason":"all CI checks green"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "disarm") {
		t.Errorf("stop during a loop turn should acknowledge, got %q", out)
	}
	stop, reason := st.ConsumeStop()
	if !stop || reason != "all CI checks green" {
		t.Fatalf("ConsumeStop = (%v,%q), want (true,\"all CI checks green\")", stop, reason)
	}
	// ConsumeStop is one-shot.
	if stop2, _ := st.ConsumeStop(); stop2 {
		t.Error("ConsumeStop should reset the flag")
	}
}

func TestLoopControl_SetTurnActiveFalseClearsStop(t *testing.T) {
	st := &LoopControlState{}
	st.SetTurnActive(true)
	(&LoopControlTool{State: st}).Execute(context.Background(), `{"action":"stop"}`)
	st.SetTurnActive(false) // turn boundary must drop an unconsumed request
	if st.IsActive() {
		t.Error("turn should be inactive")
	}
	if stop, _ := st.ConsumeStop(); stop {
		t.Error("unmarking the turn must clear a pending stop so it can't leak")
	}
}

func TestLoopControl_NilStateSafe(t *testing.T) {
	var st *LoopControlState
	if st.IsActive() {
		t.Error("nil state is never active")
	}
	st.SetTurnActive(true) // must not panic
	if stop, _ := st.ConsumeStop(); stop {
		t.Error("nil state never reports a stop")
	}
	// A tool over nil state (never happens in practice, but must not panic).
	out, _ := (&LoopControlTool{}).Execute(context.Background(), `{"action":"stop"}`)
	if !strings.Contains(out, "nothing to stop") {
		t.Errorf("nil-state tool should refuse, got %q", out)
	}
}

func TestLoopControl_UnknownAction(t *testing.T) {
	st := &LoopControlState{}
	st.SetTurnActive(true)
	out, _ := (&LoopControlTool{State: st}).Execute(context.Background(), `{"action":"pause"}`)
	if !strings.Contains(out, "unknown action") {
		t.Errorf("unknown action should be rejected, got %q", out)
	}
	if stop, _ := st.ConsumeStop(); stop {
		t.Error("unknown action must not set stop")
	}
}

func TestLoopControl_ContextRoundTrip(t *testing.T) {
	st := &LoopControlState{}
	if st.Context() != "" {
		t.Error("fresh state has no context")
	}
	st.SetContext("It runs every 1m and is unbounded.")
	if st.Context() != "It runs every 1m and is unbounded." {
		t.Errorf("context = %q", st.Context())
	}
	st.SetTurnActive(false) // turn boundary clears context too
	if st.Context() != "" {
		t.Error("SetTurnActive(false) should clear the context")
	}
}

// TestLoopControl_AddendumInjectedWhenActive drives a real Turn through a
// capturing streamer and asserts the loop-assessment addendum (and the
// loop_control tool) reach the model on a loop iteration.
func TestLoopControl_AddendumInjectedWhenActive(t *testing.T) {
	cap := &capturingStreamer{turn: []adapter.StreamEvent{sseToken("ok"), sseDone("ok")}}
	lc := &LoopControlState{}
	lc.SetContext("It runs every 1m and is unbounded.")
	lc.SetTurnActive(true)
	reg := NewRegistry()
	reg.Register(&LoopControlTool{State: lc})
	cfg := LoopConfig{Adapter: cap, Registry: reg, MaxIterations: 2, LoopControl: lc}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "check for accountants"}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(cap.calls) == 0 {
		t.Fatal("adapter never called")
	}
	first := cap.calls[0].messages
	if len(first) == 0 || first[0].Role != adapter.RoleSystem {
		t.Fatalf("expected loop addendum as first system message; got %+v", first)
	}
	add := first[0].Content
	if !strings.Contains(add, "loop_control") || !strings.Contains(add, "recurring /loop") {
		t.Errorf("addendum missing loop-control guidance; got %q", add)
	}
	if !strings.Contains(add, "It runs every 1m and is unbounded.") {
		t.Errorf("addendum should embed the loop context; got %q", add)
	}
	var hasTool bool
	for _, tl := range cap.calls[0].tools {
		if tl.Name == "loop_control" {
			hasTool = true
		}
	}
	if !hasTool {
		t.Error("loop_control should be advertised during a loop turn")
	}
}

// TestLoopControl_AddendumAbsentWhenInactive confirms the addendum and tool are
// invisible on an ordinary (non-loop) turn.
func TestLoopControl_AddendumAbsentWhenInactive(t *testing.T) {
	cap := &capturingStreamer{turn: []adapter.StreamEvent{sseToken("ok"), sseDone("ok")}}
	lc := &LoopControlState{} // not active
	reg := NewRegistry()
	reg.Register(&LoopControlTool{State: lc})
	cfg := LoopConfig{Adapter: cap, Registry: reg, MaxIterations: 2, LoopControl: lc}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	first := cap.calls[0].messages
	if len(first) > 0 && first[0].Role == adapter.RoleSystem && strings.Contains(first[0].Content, "recurring /loop") {
		t.Error("loop addendum must not appear outside a loop turn")
	}
	for _, tl := range cap.calls[0].tools {
		if tl.Name == "loop_control" {
			t.Error("loop_control must be hidden outside a loop turn")
		}
	}
}
