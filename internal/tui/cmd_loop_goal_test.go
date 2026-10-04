package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func TestParseLoopTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"200k", 200_000, true},
		{"200K", 200_000, true},
		{"1.5m", 1_500_000, true},
		{"2M", 2_000_000, true},
		{"50000", 50_000, true},
		{"0.5k", 500, true},
		{"0", 0, false},
		{"-5k", 0, false},
		{"k", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"1e9m", 0, false}, // absurdly large
	}
	for _, c := range cases {
		got, ok := parseLoopTokens(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseLoopTokens(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// armGoalLoop arms a prose loop with turnActive=true so arming does not fire the
// first iteration (newTestModel has no adapter) — the loop just arms.
func armGoalLoop(t *testing.T, m Model, args ...string) (Model, loopState) {
	t.Helper()
	m.turnActive = true
	m, _ = cmdLoop(m, args)
	return m, firstLoop(t, m)
}

func TestLoop_ParsesBudgetVerifyAndCount(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		remaining int
		budget    int64
		verify    bool
		payload   string
	}{
		{"budget only", []string{"2m", "--budget", "200k", "fix", "it"}, -1, 200_000, false, "fix it"},
		{"budget equals form", []string{"2m", "--budget=1.5m", "fix", "it"}, -1, 1_500_000, false, "fix it"},
		{"verify only", []string{"2m", "--verify", "fix", "it"}, -1, 0, true, "fix it"},
		{"count then flags", []string{"2m", "3x", "--budget", "50k", "--verify", "fix", "it"}, 3, 50_000, true, "fix it"},
		{"flags then count", []string{"2m", "--verify", "--budget", "50k", "3x", "fix", "it"}, 3, 50_000, true, "fix it"},
		{"no options is unchanged", []string{"2m", "fix", "it"}, -1, 0, false, "fix it"},
		{"count-looking word in payload stays payload", []string{"2m", "--verify", "3x", "4x", "go"}, 3, 0, true, "4x go"},
	}
	for _, c := range cases {
		m := newTestModel(t)
		m.subagentTasks = subagents.NewRegistry()
		_, ls := armGoalLoop(t, m, c.args...)
		if ls.remaining != c.remaining || ls.budget != c.budget || ls.verify != c.verify || ls.payload != c.payload {
			t.Errorf("%s: got remaining=%d budget=%d verify=%v payload=%q; want %d %d %v %q",
				c.name, ls.remaining, ls.budget, ls.verify, ls.payload, c.remaining, c.budget, c.verify, c.payload)
		}
		if ls.spent != 0 || ls.iterations != 0 || ls.paused {
			t.Errorf("%s: a fresh loop should start with no spend/iterations/pause: %+v", c.name, ls)
		}
	}
}

func TestLoop_OptionErrorsDoNotArm(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		subs bool // give the model a subagent registry
	}{
		{"budget missing value", []string{"2m", "--budget"}, "--budget needs a token count", true},
		{"budget invalid", []string{"2m", "--budget", "lots", "do", "it"}, "invalid --budget", true},
		{"budget zero", []string{"2m", "--budget=0", "do", "it"}, "invalid --budget", true},
		{"unknown option", []string{"2m", "--fast", "do", "it"}, "unknown option --fast", true},
		{"options but no payload", []string{"2m", "--verify"}, "usage:", true},
		{"verify on slash payload", []string{"2m", "--verify", "/context"}, "--verify applies to prose loops", true},
		{"verify without subagents", []string{"2m", "--verify", "do", "it"}, "--verify needs subagents", false},
		{"budget on slash payload", []string{"2m", "--budget", "10k", "/context"}, "--budget applies to prose loops", true},
	}
	for _, c := range cases {
		m := newTestModel(t)
		if c.subs {
			m.subagentTasks = subagents.NewRegistry()
		}
		m.turnActive = true
		m, _ = cmdLoop(m, c.args)
		if m.activeLoopCount() != 0 {
			t.Errorf("%s: a rejected command must not arm a loop", c.name)
		}
		if out := m.transcript.String(); !strings.Contains(out, c.want) {
			t.Errorf("%s: transcript missing %q; got %q", c.name, c.want, out)
		}
	}
}

func TestLoop_BudgetRejectedOnSlashLoop(t *testing.T) {
	// A slash loop's turn isn't owned by the loop, so nothing would ever be
	// charged to the budget; accepting the flag would be a silent no-op.
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "--budget", "10k", "/context"})
	if m.activeLoopCount() != 0 {
		t.Fatal("--budget on a slash loop must not arm")
	}
	if out := m.transcript.String(); !strings.Contains(out, "--budget applies to prose loops") {
		t.Fatalf("expected an explanation, got %q", out)
	}
}

func TestLoop_VerifyWithoutBudgetOrCountWarns(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantWarn bool
	}{
		{"unbounded verify", []string{"2m", "--verify", "fix", "it"}, true},
		{"with budget", []string{"2m", "--verify", "--budget", "100k", "fix", "it"}, false},
		{"with count", []string{"2m", "3x", "--verify", "fix", "it"}, false},
		{"no verify", []string{"2m", "fix", "it"}, false},
	}
	for _, c := range cases {
		m := newTestModel(t)
		m.subagentTasks = subagents.NewRegistry()
		m, _ = armGoalLoop(t, m, c.args...)
		got := strings.Contains(m.transcript.String(), "consider --budget")
		if got != c.wantWarn {
			t.Errorf("%s: warning shown = %v, want %v", c.name, got, c.wantWarn)
		}
	}
}

// finalVerifyIteration arms `/loop 2m 1x --verify` against a stub adapter so the
// single (final) iteration really starts a turn, and returns the tool the model
// would use to stop it.
func finalVerifyIteration(t *testing.T) (Model, *agent.LoopControlState, *agent.LoopControlTool, *subagents.Registry) {
	t.Helper()
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	lc := &agent.LoopControlState{}
	reg := subagents.NewRegistry()
	m.cfg.LoopControl, m.subagentTasks = lc, reg
	m, _ = cmdLoop(m, []string{"2m", "1x", "--verify", "fix", "it"})
	if !m.turnActive || m.activeLoopCount() != 0 {
		t.Fatalf("the single iteration should be running and the loop already removed (turnActive=%v, armed=%d)", m.turnActive, m.activeLoopCount())
	}
	if !m.loopTurnFinalVerify {
		t.Fatal("a bounded verify loop's last iteration must be remembered as such")
	}
	return m, lc, &agent.LoopControlTool{State: lc, Tasks: reg}, reg
}

func TestLoop_FinalVerifyIterationDoesNotEndSilently(t *testing.T) {
	// The agent never reports done.
	m, _, _, _ := finalVerifyIteration(t)
	m.turnActive = false
	m.consumeLoopControl()
	if out := m.transcript.String(); !strings.Contains(out, "ended after its final iteration without a verified stop") {
		t.Fatalf("expected an unverified-end notice, got %q", out)
	}
	if m.loopTurnFinalVerify {
		t.Error("the final-verify mark must be cleared at turn end")
	}

	// The agent tried to stop but verification never passed: say why.
	m, _, tool, _ := finalVerifyIteration(t)
	if _, err := tool.Execute(context.Background(), `{"action":"stop","reason":"done"}`); err != nil {
		t.Fatal(err)
	}
	m.turnActive = false
	m.consumeLoopControl()
	if out := m.transcript.String(); !strings.Contains(out, "without a verified stop") || !strings.Contains(out, "stop refused: no verification this iteration") {
		t.Fatalf("notice should carry the refusal reason, got %q", out)
	}
}

func TestLoop_FinalVerifyIterationVerifiedStopIsReportedNormally(t *testing.T) {
	m, lc, tool, reg := finalVerifyIteration(t)
	reg.Add(&subagents.Task{ID: "v1", AgentType: "verification", Started: time.Now().Add(time.Second), Status: subagents.TaskRunning})
	reg.MarkDone("v1", subagents.TaskCompleted, "ok\nVERDICT: PASS", false, 0)
	if !lc.Verify() {
		t.Fatal("gate should be on for the final iteration")
	}
	if _, err := tool.Execute(context.Background(), `{"action":"stop","reason":"fixed and verified"}`); err != nil {
		t.Fatal(err)
	}
	m.turnActive = false
	m.consumeLoopControl()
	out := m.transcript.String()
	if !strings.Contains(out, "stopped by the agent: fixed and verified") || strings.Contains(out, "without a verified stop") {
		t.Fatalf("a verified stop on the last iteration is a normal stop, got %q", out)
	}
}

func TestLoop_FinalIterationWithoutVerifyIsUnchanged(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cfg.LoopControl = &agent.LoopControlState{}
	m, _ = cmdLoop(m, []string{"2m", "1x", "fix", "it"})
	if m.loopTurnFinalVerify {
		t.Fatal("only verify-gated loops are tracked as final-verify")
	}
	m.turnActive = false
	m.consumeLoopControl()
	if strings.Contains(m.transcript.String(), "without a verified stop") {
		t.Fatal("a plain bounded loop ends quietly, as before")
	}
}

func TestLoop_PauseStopsFiringButKeepsCadence(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	id := firstLoop(t, m).id
	m.turnActive = false

	m, _ = cmdLoop(m, []string{"pause"})
	if ls := m.loops[id]; !ls.paused || !ls.active {
		t.Fatalf("pause should keep the loop armed but paused: %+v", ls)
	}
	if !strings.Contains(m.transcript.String(), "paused") {
		t.Error("pause should print a notice")
	}

	m2, cmd := applyMsg(m, loopTickMsg{id: id})
	if cmd == nil {
		t.Fatal("a paused loop must keep re-arming its heartbeat so resume picks the cadence back up")
	}
	if got := m2.loops[id]; got.iterations != 0 {
		t.Fatalf("a paused loop must not fire; iterations = %d", got.iterations)
	}
}

func TestLoop_ResumeFiresImmediatelyWhenIdle(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"1h", "/help"})
	id := firstLoop(t, m).id
	m.turnActive = false
	m, _ = cmdLoop(m, []string{"pause", id})
	m, _ = cmdLoop(m, []string{"resume", id})
	ls := m.loops[id]
	if ls.paused {
		t.Fatal("resume should clear the paused flag")
	}
	if ls.iterations != 1 {
		t.Fatalf("resume should fire right away instead of waiting out a 1h interval; iterations = %d", ls.iterations)
	}
}

func TestLoop_ResumeDuringTurnDoesNotStackTurns(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"1h", "/help"})
	id := firstLoop(t, m).id
	m, _ = cmdLoop(m, []string{"pause", id})
	m, _ = cmdLoop(m, []string{"resume", id}) // turnActive still true
	if ls := m.loops[id]; ls.paused || ls.iterations != 0 {
		t.Fatalf("resume mid-turn clears the flag but waits for the next tick: %+v", ls)
	}
}

func TestLoop_PauseResumeTargeting(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"pause"})
	if !strings.Contains(m.transcript.String(), "nothing to pause") {
		t.Error("pause with no loops should say so")
	}
	m, _ = cmdLoop(m, []string{"resume"})
	if !strings.Contains(m.transcript.String(), "nothing to resume") {
		t.Error("resume with no loops should say so")
	}

	m.turnActive = true
	m, _ = cmdLoop(m, []string{"1m", "/help"})
	m, _ = cmdLoop(m, []string{"2m", "/context"})
	ids := m.activeLoopIDs()
	if len(ids) != 2 {
		t.Fatalf("want 2 loops, got %d", len(ids))
	}

	before := m.transcript.String()
	m, _ = cmdLoop(m, []string{"pause"})
	if !strings.Contains(m.transcript.String()[len(before):], "multiple loops active") {
		t.Error("pause with several loops needs an ID or all")
	}
	m, _ = cmdLoop(m, []string{"pause", "loop-nope"})
	if !strings.Contains(m.transcript.String(), `no active loop "loop-nope"`) {
		t.Error("pause with a bad ID should refuse")
	}

	m, _ = cmdLoop(m, []string{"pause", ids[0]})
	if !m.loops[ids[0]].paused || m.loops[ids[1]].paused {
		t.Fatal("pause <id> should affect only that loop")
	}
	before = m.transcript.String()
	m, _ = cmdLoop(m, []string{"pause", ids[0]})
	if !strings.Contains(m.transcript.String()[len(before):], "already paused") {
		t.Error("pausing a paused loop should say so")
	}
	m, _ = cmdLoop(m, []string{"pause", "all"})
	if !m.loops[ids[0]].paused || !m.loops[ids[1]].paused {
		t.Fatal("pause all should pause every loop")
	}
	m, _ = cmdLoop(m, []string{"resume", "all"})
	if m.loops[ids[0]].paused || m.loops[ids[1]].paused {
		t.Fatal("resume all should resume every loop")
	}
}

func TestLoop_PauseDoesNotCancelRunningIteration(t *testing.T) {
	m := newTestModel(t)
	m, ls := armGoalLoop(t, m, "2m", "work", "on", "it")
	cancelled := false
	m.currentLoopTurnID = ls.id
	m.turnCancel = func() { cancelled = true }
	m, _ = cmdLoop(m, []string{"pause"})
	if cancelled {
		t.Fatal("pausing prevents the NEXT iteration; it must not cancel the one running")
	}
}

func TestLoop_PausedWordAsPayloadStillArms(t *testing.T) {
	// The verbs are recognised before interval parsing, like `stop`.
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "pause", "the", "deploy"})
	if ls := firstLoop(t, m); ls.payload != "pause the deploy" || ls.paused {
		t.Fatalf("prose payload starting with pause: %+v", ls)
	}
}

func TestLoop_BudgetMetersOnlyTheLoopsOwnTurns(t *testing.T) {
	m := newTestModel(t)
	m.cfg.LoopControl = &agent.LoopControlState{}
	m, ls := armGoalLoop(t, m, "2m", "--budget", "200k", "fix", "the", "bug")
	id := ls.id

	// Spend that happened before the loop's turn started (the user's own turns,
	// another loop) is not charged to this loop.
	m.sess.TotalUsage.InputTokens += 500_000
	m.currentLoopTurnID, m.loopTurnTokens = id, m.sessionTokens()
	m.sess.TotalUsage.InputTokens += 150_000
	m.turnActive = false
	m.consumeLoopControl()
	if got := m.loops[id]; got.spent != 150_000 {
		t.Fatalf("spent = %d, want 150000 (only the loop-owned turn)", got.spent)
	}
	if m.activeLoopCount() != 1 {
		t.Fatal("under budget → the loop keeps running")
	}

	// A turn that is not loop-owned never charges anything.
	m.sess.TotalUsage.InputTokens += 900_000
	m.consumeLoopControl()
	if got := m.loops[id]; got.spent != 150_000 {
		t.Fatalf("a non-loop turn changed spent to %d", got.spent)
	}

	// The next iteration crosses the budget: the loop disarms between iterations.
	m.currentLoopTurnID, m.loopTurnTokens = id, m.sessionTokens()
	m.sess.TotalUsage.InputTokens += 60_000
	m.consumeLoopControl()
	if m.activeLoopCount() != 0 {
		t.Fatal("reaching the budget should disarm the loop")
	}
	if out := m.transcript.String(); !strings.Contains(out, "token budget reached") || !strings.Contains(out, "210K of 200K") {
		t.Fatalf("expected a budget notice with the totals; got %q", out)
	}
}

// TestLoop_BudgetCountsSubagentSpendDuringTheTurn: a subagent the loop's turn
// ran is charged to the loop even though sess.SubagentTasks (the persisted
// snapshot) is only refreshed after consumeLoopControl — the meter must read the
// live registry.
func TestLoop_BudgetCountsSubagentSpendDuringTheTurn(t *testing.T) {
	m := newTestModel(t)
	reg := subagents.NewRegistry()
	m.subagentTasks = reg
	m.cfg.LoopControl = &agent.LoopControlState{}
	reg.Add(&subagents.Task{ID: "pre", AgentType: "explore", Started: time.Now(), Status: subagents.TaskRunning})
	reg.AddUsage("pre", &adapter.Usage{InputTokens: 700_000}) // earlier work, not this loop's
	m, ls := armGoalLoop(t, m, "2m", "--budget", "100k", "fix", "it")

	m.currentLoopTurnID, m.loopTurnTokens = ls.id, totalTokensFor(m.sess.TotalUsage)
	m.loopTurnSubagentTokens = m.loopSubagentTokens()
	reg.Add(&subagents.Task{ID: "v1", AgentType: "verification", Started: time.Now(), Status: subagents.TaskRunning})
	reg.AddUsage("v1", &adapter.Usage{InputTokens: 80_000, OutputTokens: 5_000})
	m.sess.TotalUsage.InputTokens += 10_000 // main-thread spend in the same turn
	m.consumeLoopControl()

	got, ok := m.loops[ls.id]
	if !ok {
		t.Fatal("95K of 100K is under budget; the loop should still be armed")
	}
	if got.spent != 95_000 {
		t.Fatalf("spent = %d, want 95000 (80K+5K subagent, 10K main; the 700K before the turn excluded)", got.spent)
	}
}

func TestLoop_SessionTokensWithoutRegistryOrSession(t *testing.T) {
	m := newTestModel(t) // no live registry, no recorded subagents
	m.sess.TotalUsage.InputTokens = 1_000
	if got := m.sessionTokens(); got != 1_000 {
		t.Fatalf("sessionTokens = %d, want 1000", got)
	}
	var nilSess Model
	if nilSess.sessionTokens() != 0 {
		t.Error("a model without a session reads as zero tokens")
	}
}

func TestLoop_NoBudgetNeverDisarmsOnSpend(t *testing.T) {
	m := newTestModel(t)
	m.cfg.LoopControl = &agent.LoopControlState{}
	m, ls := armGoalLoop(t, m, "2m", "work")
	m.currentLoopTurnID, m.loopTurnTokens = ls.id, totalTokensFor(m.sess.TotalUsage)
	m.sess.TotalUsage.InputTokens += 5_000_000
	m.consumeLoopControl()
	if m.activeLoopCount() != 1 {
		t.Fatal("a loop without --budget must never stop on spend")
	}
	if got := m.loops[ls.id].spent; got != 5_000_000 {
		t.Errorf("spend should still be tracked for the status row, got %d", got)
	}
}

func TestLoop_StopRequestBeatsBudgetNotice(t *testing.T) {
	m := newTestModel(t)
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	m, ls := armGoalLoop(t, m, "2m", "--budget", "1k", "work")
	m.currentLoopTurnID, m.loopTurnTokens = ls.id, totalTokensFor(m.sess.TotalUsage)
	lc.SetTurnActive(true)
	(&agent.LoopControlTool{State: lc}).Execute(context.Background(), `{"action":"stop","reason":"done"}`)
	m.sess.TotalUsage.InputTokens += 50_000
	m.consumeLoopControl()
	out := m.transcript.String()
	if !strings.Contains(out, "stopped by the agent: done") || strings.Contains(out, "budget reached") {
		t.Fatalf("an agent stop is reported as such, not as a budget stop: %q", out)
	}
}

// TestLoop_VerifyGateRoundTrip drives a --verify loop through the real tool and
// consumeLoopControl: a refused stop leaves the loop armed with a status note, a
// later PASS lets it stop, and a blocker is labelled unverified.
func TestLoop_VerifyGateRoundTrip(t *testing.T) {
	m := newTestModel(t)
	lc := &agent.LoopControlState{}
	reg := subagents.NewRegistry()
	m.cfg.LoopControl = lc
	m.subagentTasks = reg
	tool := &agent.LoopControlTool{State: lc, Tasks: reg}
	m, ls := armGoalLoop(t, m, "2m", "--verify", "fix", "the", "bug")
	id := ls.id

	runIteration := func(args string, verifications func(start time.Time)) {
		start := time.Now()
		lc.SetContext(loopTurnContext(m.loops[id]))
		lc.SetVerify(true, start)
		lc.SetTurnActive(true)
		m.currentLoopTurnID = id
		if verifications != nil {
			verifications(start)
		}
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		m.turnActive = false
		m.consumeLoopControl()
	}

	// Iteration 1: the model tries to stop with no verification → refused.
	runIteration(`{"action":"stop","reason":"done"}`, nil)
	if m.activeLoopCount() != 1 {
		t.Fatal("a refused stop must leave the loop armed")
	}
	if note := m.loops[id].lastNote; !strings.Contains(note, "no verification this iteration") {
		t.Fatalf("lastNote = %q", note)
	}
	if ctx := loopTurnContext(m.loops[id]); !strings.Contains(ctx, "Last stop attempt: stop refused") {
		t.Errorf("the next iteration should be told why the last stop failed: %q", ctx)
	}
	if panel := m.renderLoopListPanel(); !strings.Contains(panel, "verify") {
		t.Errorf("status row should show the verify gate: %q", panel)
	}

	// Iteration 2: verification returns FAIL → still armed, note updated.
	runIteration(`{"action":"stop"}`, func(start time.Time) {
		reg.Add(&subagents.Task{ID: "v1", AgentType: "verification", Started: start.Add(time.Millisecond), Status: subagents.TaskRunning})
		reg.MarkDone("v1", subagents.TaskCompleted, "broke on empty input\nVERDICT: FAIL", false, 0)
	})
	if m.activeLoopCount() != 1 || !strings.Contains(m.loops[id].lastNote, "FAIL") {
		t.Fatalf("a FAIL must keep the loop running with its note: armed=%d note=%q", m.activeLoopCount(), m.loops[id].lastNote)
	}

	// Iteration 3: PASS → the stop goes through and the stale note is moot.
	runIteration(`{"action":"stop","reason":"fixed and verified"}`, func(start time.Time) {
		reg.Add(&subagents.Task{ID: "v2", AgentType: "verification", Started: start.Add(time.Millisecond), Status: subagents.TaskRunning})
		reg.MarkDone("v2", subagents.TaskCompleted, "ok\nVERDICT: PASS", false, 0)
	})
	if m.activeLoopCount() != 0 {
		t.Fatal("a PASS should let the loop stop")
	}
	if out := m.transcript.String(); !strings.Contains(out, "stopped by the agent: fixed and verified") || strings.Contains(out, "UNVERIFIED") {
		t.Fatalf("verified stop notice wrong: %q", out)
	}
}

func TestLoop_BlockedStopIsLabelledUnverified(t *testing.T) {
	m := newTestModel(t)
	lc := &agent.LoopControlState{}
	reg := subagents.NewRegistry()
	m.cfg.LoopControl = lc
	m.subagentTasks = reg
	m, ls := armGoalLoop(t, m, "2m", "--verify", "deploy", "it")
	lc.SetVerify(true, time.Now())
	lc.SetTurnActive(true)
	m.currentLoopTurnID = ls.id
	if _, err := (&agent.LoopControlTool{State: lc, Tasks: reg}).Execute(context.Background(), `{"action":"stop","reason":"deploy key revoked","blocked":true}`); err != nil {
		t.Fatal(err)
	}
	m.turnActive = false
	m.consumeLoopControl()
	if m.activeLoopCount() != 0 {
		t.Fatal("a blocked stop should disarm")
	}
	if out := m.transcript.String(); !strings.Contains(out, "UNVERIFIED — blocked") || !strings.Contains(out, "deploy key revoked") {
		t.Fatalf("blocked stop must be labelled unverified with its reason: %q", out)
	}
}

func TestLoop_FireIterationArmsVerifyGateAndContext(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	m.subagentTasks = subagents.NewRegistry()
	m, _ = cmdLoop(m, []string{"2m", "--budget", "100k", "--verify", "fix", "it"})
	if !m.turnActive {
		t.Fatal("the prose iteration should have started a turn")
	}
	if !lc.Verify() {
		t.Error("a --verify loop's iteration must turn the stop gate on")
	}
	ctx := lc.Context()
	for _, want := range []string{"token budget", "0 of 100K", "verify-gated", "VERDICT: PASS", "blocked: true"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("loop context missing %q: %q", want, ctx)
		}
	}
	if got := firstLoop(t, m).iterations; got != 1 {
		t.Errorf("iterations = %d, want 1", got)
	}

	// A loop without --verify leaves the gate off.
	m2 := newTestModel(t)
	m2.cfg.Adapter = stubAdapterNoStream{}
	lc2 := &agent.LoopControlState{}
	m2.cfg.LoopControl = lc2
	m2, _ = cmdLoop(m2, []string{"2m", "fix", "it"})
	if lc2.Verify() {
		t.Error("a loop without --verify must not be gated")
	}
}

func TestLoopTurnContext_BudgetAndVerifySentences(t *testing.T) {
	plain := loopTurnContext(loopState{interval: time.Minute, remaining: -1})
	if strings.Contains(plain, "budget") || strings.Contains(plain, "verify") {
		t.Errorf("a plain loop's context should not mention budget/verify: %q", plain)
	}
	ctx := loopTurnContext(loopState{interval: time.Minute, remaining: -1, budget: 200_000, spent: 45_000, verify: true, lastNote: "stop refused: verification FAIL"})
	for _, want := range []string{"45K of 200K", "verify-gated", "Last stop attempt: stop refused: verification FAIL"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context missing %q: %q", want, ctx)
		}
	}
}

func TestLoopStatusBits(t *testing.T) {
	if bits := loopStatusBits(loopState{}); len(bits) != 0 {
		t.Errorf("a fresh loop has no extra status: %v", bits)
	}
	full := loopStatusBits(loopState{paused: true, iterations: 3, budget: 200_000, spent: 45_000, verify: true, lastNote: "stop refused: verification FAIL"})
	want := []string{"paused", "iter 3", "45K/200K tokens", "verify", "stop refused: verification FAIL"}
	if strings.Join(full, "|") != strings.Join(want, "|") {
		t.Errorf("bits = %v, want %v", full, want)
	}
	if bits := loopStatusBits(loopState{spent: 1500}); len(bits) != 1 || bits[0] != "1.5K tokens" {
		t.Errorf("spend without a budget shows a plain count: %v", bits)
	}
}

func TestLoop_StatusSurfacesShowPauseAndMeter(t *testing.T) {
	m := newTestModel(t)
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, interval: time.Minute, remaining: -1, payload: "work", paused: true, budget: 200_000, spent: 10_000, iterations: 2, expiresAt: time.Now().Add(time.Hour)}}
	m.loopOrder = []string{"loop-a"}

	if panel := m.renderLoopListPanel(); !strings.Contains(panel, "paused") || !strings.Contains(panel, "pause|resume") {
		t.Errorf("status panel should show the pause and offer pause/resume: %q", panel)
	}
	if card := renderLoopCard(m.loops["loop-a"], 100); !strings.Contains(card, "10K/200K tokens") || !strings.Contains(card, "iter 2") {
		t.Errorf("card should show the meter and iterations: %q", card)
	}
	if banner := renderLoopBanner(m.loopBannerStates(), 100); !strings.Contains(banner, "paused") || strings.Contains(banner, "every 1m") {
		t.Errorf("banner should say paused instead of the cadence: %q", banner)
	}
}

// TestLoop_EndToEndThroughTurnEndedMsg drives a budgeted loop through the real
// Update path: typing the command fires iteration 1 (a real turn start against a
// stub adapter), then a turnEndedMsg runs the actual turn-end handler. It guards
// the wiring the direct consumeLoopControl tests can't: that the handler calls
// it, and that spend and the stop request are read before the turn state resets.
func TestLoop_EndToEndThroughTurnEndedMsg(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cfg.LoopControl = &agent.LoopControlState{}
	m, _ = typeAndEnter(t, m, "/loop 1m --budget 100k fix the flaky test")
	ls := firstLoop(t, m)
	if !m.turnActive || m.currentLoopTurnID != ls.id || ls.budget != 100_000 || ls.iterations != 1 {
		t.Fatalf("iteration 1 should be running and metered: turnActive=%v owner=%q ls=%+v", m.turnActive, m.currentLoopTurnID, ls)
	}

	m.sess.TotalUsage.InputTokens += 130_000 // the iteration overspends
	m, _ = applyMsg(m, turnEndedMsg{})

	if m.activeLoopCount() != 0 {
		t.Fatal("the turn-end handler should have disarmed the loop at budget")
	}
	if out := m.transcript.String(); !strings.Contains(out, "token budget reached (130K of 100K used)") {
		t.Fatalf("expected the budget notice from the real handler; got %q", out)
	}
	if m.currentLoopTurnID != "" || m.cfg.LoopControl.IsActive() {
		t.Error("turn-end must reset the loop turn state")
	}
}
