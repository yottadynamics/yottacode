package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// gateFixture is a verify-gated loop iteration: turn active, gate on, with a
// registry the verification runs are recorded in. start is when the iteration
// began; runs started before it must not count.
type gateFixture struct {
	state *LoopControlState
	reg   *subagents.Registry
	tool  *LoopControlTool
	start time.Time
}

func newGateFixture(t *testing.T) gateFixture {
	t.Helper()
	st := &LoopControlState{}
	reg := subagents.NewRegistry()
	start := time.Now().Add(-time.Minute)
	st.SetTurnActive(true)
	st.SetVerify(true, start)
	return gateFixture{state: st, reg: reg, tool: &LoopControlTool{State: st, Tasks: reg}, start: start}
}

// addRun records a verification run started offset after the iteration began.
// A nil status leaves it Running; otherwise it is marked done with result.
func (f gateFixture) addRun(id string, offset time.Duration, status *subagents.TaskStatus, result string) {
	f.reg.Add(&subagents.Task{ID: id, AgentType: "verification", Started: f.start.Add(offset), Status: subagents.TaskRunning})
	if status != nil {
		f.reg.MarkDone(id, *status, result, *status == subagents.TaskErrored, 0)
	}
}

func (f gateFixture) stop(t *testing.T, args string) string {
	t.Helper()
	out, err := f.tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func statusPtr(s subagents.TaskStatus) *subagents.TaskStatus { return &s }

func TestLoopControlGate_RefusesWithoutVerification(t *testing.T) {
	f := newGateFixture(t)
	out := f.stop(t, `{"action":"stop","reason":"done"}`)
	if !strings.Contains(out, "stop refused") || !strings.Contains(out, "no verification has run this iteration") {
		t.Fatalf("expected refusal, got %q", out)
	}
	if stop, _ := f.state.ConsumeStop(); stop {
		t.Fatal("a refused stop must not be recorded")
	}
	if note := f.state.ConsumeGateNote(); !strings.Contains(note, "no verification this iteration") {
		t.Fatalf("gate note = %q", note)
	}
	if f.state.ConsumeGateNote() != "" {
		t.Error("gate note should be consumed once")
	}
}

func TestLoopControlGate_PassOpensTheGate(t *testing.T) {
	f := newGateFixture(t)
	f.addRun("v1", time.Second, statusPtr(subagents.TaskCompleted), "### Check: build\nok\n\nVERDICT: PASS")
	out := f.stop(t, `{"action":"stop","reason":"goal met and verified"}`)
	if strings.Contains(out, "refused") {
		t.Fatalf("PASS should accept the stop, got %q", out)
	}
	stop, reason, blocked := f.state.ConsumeStopDetail()
	if !stop || blocked || reason != "goal met and verified" {
		t.Fatalf("stop=%v blocked=%v reason=%q", stop, blocked, reason)
	}
}

func TestLoopControlGate_FailAndPartialRefuseAndEchoReport(t *testing.T) {
	for _, verdict := range []string{"FAIL", "PARTIAL"} {
		f := newGateFixture(t)
		f.addRun("v1", time.Second, statusPtr(subagents.TaskCompleted), "### Check: POST /x\nreturned 500\n\nVERDICT: "+verdict)
		out := f.stop(t, `{"action":"stop"}`)
		if !strings.Contains(out, "verification returned "+verdict) || !strings.Contains(out, "returned 500") {
			t.Errorf("%s: refusal should name the verdict and echo the report, got %q", verdict, out)
		}
		if !strings.Contains(out, "previous FAIL findings") {
			t.Errorf("%s: refusal should tell the model to pass findings to the re-check", verdict)
		}
		if stop, _ := f.state.ConsumeStop(); stop {
			t.Errorf("%s: refused stop was recorded", verdict)
		}
		if note := f.state.ConsumeGateNote(); !strings.Contains(note, verdict) {
			t.Errorf("%s: gate note = %q", verdict, note)
		}
	}
}

func TestLoopControlGate_ReportTailIsCapped(t *testing.T) {
	f := newGateFixture(t)
	long := strings.Repeat("x", verifyGateTail*3) + "\nTHE-END\nVERDICT: FAIL"
	f.addRun("v1", time.Second, statusPtr(subagents.TaskCompleted), long)
	out := f.stop(t, `{"action":"stop"}`)
	if len(out) > verifyGateTail+1500 {
		t.Errorf("refusal is %d bytes; the report tail should be capped near %d", len(out), verifyGateTail)
	}
	if !strings.Contains(out, "THE-END") {
		t.Error("the tail (not the head) of the report should be kept")
	}
}

func TestLoopControlGate_NoVerdictLineRefuses(t *testing.T) {
	f := newGateFixture(t)
	f.addRun("v1", time.Second, statusPtr(subagents.TaskCompleted), "Everything looks great. PASS from me.")
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "no verdict line") {
		t.Fatalf("a result without a VERDICT line must not open the gate, got %q", out)
	}
}

func TestLoopControlGate_RunBeforeIterationDoesNotCount(t *testing.T) {
	f := newGateFixture(t)
	// A PASS from before this iteration began says nothing about its changes.
	f.addRun("old", -30*time.Second, statusPtr(subagents.TaskCompleted), "VERDICT: PASS")
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "no verification has run this iteration") {
		t.Fatalf("stale PASS opened the gate: %q", out)
	}
}

func TestLoopControlGate_LatestRunWins(t *testing.T) {
	f := newGateFixture(t)
	f.addRun("v1", time.Second, statusPtr(subagents.TaskCompleted), "VERDICT: PASS")
	f.addRun("v2", 2*time.Second, statusPtr(subagents.TaskCompleted), "regressed\nVERDICT: FAIL")
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "returned FAIL") {
		t.Fatalf("the newer FAIL should win over the older PASS: %q", out)
	}
}

func TestLoopControlGate_RunningAndUnfinishedRefuse(t *testing.T) {
	f := newGateFixture(t)
	f.addRun("v1", time.Second, nil, "")
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "still in progress") {
		t.Fatalf("running verification: %q", out)
	}
	for _, status := range []subagents.TaskStatus{subagents.TaskErrored, subagents.TaskCanceled, subagents.TaskIterCapped} {
		f := newGateFixture(t)
		f.addRun("v1", time.Second, statusPtr(status), "VERDICT: PASS")
		if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "did not complete") {
			t.Errorf("status %s must not open the gate even with a PASS line: %q", status, out)
		}
	}
}

func TestLoopControlGate_IgnoresOtherAgentsAndHistoricalRuns(t *testing.T) {
	f := newGateFixture(t)
	f.reg.Add(&subagents.Task{ID: "r1", AgentType: "review", Started: f.start.Add(time.Second), Status: subagents.TaskRunning})
	f.reg.MarkDone("r1", subagents.TaskCompleted, "VERDICT: PASS", false, 0)
	f.reg.Add(&subagents.Task{ID: "h1", AgentType: "verification", Historical: true, Started: f.start.Add(time.Second), Status: subagents.TaskRunning})
	f.reg.MarkDone("h1", subagents.TaskCompleted, "VERDICT: PASS", false, 0)
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "no verification has run this iteration") {
		t.Fatalf("only a live verification run may open the gate: %q", out)
	}
}

func TestLoopControlGate_NilRegistryRefuses(t *testing.T) {
	f := newGateFixture(t)
	f.tool.Tasks = nil
	if out := f.stop(t, `{"action":"stop"}`); !strings.Contains(out, "stop refused") || !strings.Contains(out, "blocked: true") {
		t.Fatalf("no registry should refuse and point at the blocked escape, got %q", out)
	}
}

func TestLoopControlGate_BlockedBypassesAsUnverified(t *testing.T) {
	f := newGateFixture(t)
	out := f.stop(t, `{"action":"stop","reason":"deploy key was revoked","blocked":true}`)
	if !strings.Contains(out, "UNVERIFIED") {
		t.Fatalf("blocked stop should say it is unverified, got %q", out)
	}
	stop, reason, blocked := f.state.ConsumeStopDetail()
	if !stop || !blocked || reason != "deploy key was revoked" {
		t.Fatalf("stop=%v blocked=%v reason=%q", stop, blocked, reason)
	}
}

func TestLoopControlGate_BlockedIsNoOpWithoutVerify(t *testing.T) {
	st := &LoopControlState{}
	st.SetTurnActive(true) // not verify-gated
	tool := &LoopControlTool{State: st}
	if _, err := tool.Execute(context.Background(), `{"action":"stop","blocked":true}`); err != nil {
		t.Fatal(err)
	}
	stop, _, blocked := st.ConsumeStopDetail()
	if !stop || blocked {
		t.Fatalf("an ungated loop stops normally and is never labeled unverified: stop=%v blocked=%v", stop, blocked)
	}
}

func TestLoopControlGate_UngatedLoopNeedsNoVerification(t *testing.T) {
	st := &LoopControlState{}
	st.SetTurnActive(true)
	tool := &LoopControlTool{State: st, Tasks: subagents.NewRegistry()}
	if out, _ := tool.Execute(context.Background(), `{"action":"stop","reason":"ci green"}`); strings.Contains(out, "refused") {
		t.Fatalf("a loop without --verify must stop freely, got %q", out)
	}
	if stop, _ := st.ConsumeStop(); !stop {
		t.Fatal("stop not recorded")
	}
}

func TestLoopControlState_TurnEndClearsVerifyGate(t *testing.T) {
	f := newGateFixture(t)
	f.state.recordGateNote("stop refused: x")
	f.state.SetTurnActive(false)
	if f.state.Verify() {
		t.Error("verify gate must not leak into the next turn")
	}
	if f.state.ConsumeGateNote() != "" {
		t.Error("gate note must not leak into the next turn")
	}
	var nilState *LoopControlState
	nilState.SetVerify(true, time.Now()) // nil-safe
	if nilState.Verify() || nilState.ConsumeGateNote() != "" {
		t.Error("nil state should read as ungated")
	}
}

func TestLoopControlTool_SchemaAndDescriptionAdvertiseGate(t *testing.T) {
	tool := &LoopControlTool{State: &LoopControlState{}}
	props := tool.Schema()["properties"].(map[string]any)
	if _, ok := props["blocked"]; !ok {
		t.Error("schema should expose the blocked escape hatch")
	}
	desc := tool.Description()
	for _, want := range []string{"--verify", "VERDICT: PASS", "blocked: true"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q", want)
		}
	}
	if got := tool.PreviewCall(`{"action":"stop","reason":"x","blocked":true}`); !strings.Contains(got, "stop") {
		t.Errorf("preview = %q", got)
	}
}

func TestLoopIterationAddendum_TaskDisciplinePinned(t *testing.T) {
	for _, want := range []string{
		"GET SOMETHING DONE",
		"Tool call first, narration second",
		"Don't ask permission to continue work already in flight",
		"todo_write",
		"easy, unblocked work",
		"exact blocker",
	} {
		if !strings.Contains(LoopIterationAddendum, want) {
			t.Errorf("loop addendum missing %q", want)
		}
	}
	// Still a single-%s template: the loop descriptor is the only verb.
	if got := strings.Count(LoopIterationAddendum, "%"); got != 1 {
		t.Errorf("addendum has %d %% verbs, want exactly the one loop descriptor", got)
	}
	if rendered := strings.ReplaceAll(LoopIterationAddendum, "%s", "CTX"); strings.Contains(rendered, "%!") {
		t.Error("addendum does not render cleanly")
	}
}
