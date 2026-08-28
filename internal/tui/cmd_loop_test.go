package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

func firstLoop(t *testing.T, m Model) loopState {
	t.Helper()
	ids := m.activeLoopIDs()
	if len(ids) != 1 {
		t.Fatalf("active loops = %d, want 1", len(ids))
	}
	return m.loops[ids[0]]
}

func TestParseLoopInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"5m", 5 * time.Minute, true},
		{"30s", 30 * time.Second, true},
		{"1h", time.Hour, true},
		{"check", 0, false},
		{"3x", 0, false},
		{"0s", 0, false},
		{"-5m", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseLoopInterval(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLoopInterval(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseLoopCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"3x", 3, true}, {"10X", 10, true}, {"1x", 1, true},
		{"x", 0, false}, {"0x", 0, false}, {"-2x", 0, false}, {"5m", 0, false}, {"abc", 0, false}, {"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseLoopCount(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLoopCount(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLoop_ArmsIntervalState(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, cmd := cmdLoop(m, []string{"30s", "3x", "summarize", "open", "work"})
	ls := firstLoop(t, m)
	if ls.interval != 30*time.Second || ls.remaining != 3 || ls.payload != "summarize open work" || ls.isSlash {
		t.Fatalf("loop state = %+v", ls)
	}
	if ls.id == "" || ls.expiresAt.Sub(ls.armedAt) != loopDefaultTTL {
		t.Fatalf("id/expiry not set: %+v", ls)
	}
	if cmd == nil {
		t.Error("arming an interval loop should schedule a tick cmd")
	}
}

func TestLoop_MultipleLoopsAndStatus(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	m, _ = cmdLoop(m, []string{"2m", "check", "ci"})
	if got := m.activeLoopCount(); got != 2 {
		t.Fatalf("active loops = %d, want 2", got)
	}
	ids := m.activeLoopIDs()
	if ids[0] == ids[1] {
		t.Fatal("loop IDs should be unique")
	}
	// Bare /loop opens the status panel above the cmdline instead of writing
	// cards into the transcript.
	before := m.transcript.String()
	m, _ = cmdLoop(m, nil)
	if !m.loopListOpen {
		t.Fatal("bare /loop should open the loop-list panel")
	}
	if m.transcript.String() != before {
		t.Fatalf("bare /loop must not write to the transcript; got %q", m.transcript.String())
	}
	panel := stripANSI(m.renderLoopListPanel())
	if !strings.Contains(panel, ids[0]) || !strings.Contains(panel, ids[1]) {
		t.Fatalf("panel should include both IDs; got %q", panel)
	}
	if !strings.Contains(panel, "2 active loops") {
		t.Fatalf("panel should show the active count; got %q", panel)
	}
	if !strings.Contains(panel, "any key to dismiss") {
		t.Fatalf("panel should show the dismiss hint; got %q", panel)
	}
	// It's a compact menu, not the multi-line arm card — no gutter glyphs.
	if strings.ContainsAny(panel, "╭│╰") {
		t.Fatalf("the /loop panel should be a menu, not cards; got %q", panel)
	}
	// Any key dismisses the panel without disarming the loops.
	m2, _ := applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m2.loopListOpen || m2.activeLoopCount() != 2 {
		t.Fatalf("Esc should close the panel and keep both loops (open=%v, count=%d)", m2.loopListOpen, m2.activeLoopCount())
	}
}

func TestLoop_IntervalSlashPayloadRouted(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/git-review-pr"})
	ls := firstLoop(t, m)
	if !ls.isSlash || ls.interval != 30*time.Second || ls.payload != "/git-review-pr" {
		t.Fatalf("loop state = %+v", ls)
	}
}

func TestLoop_IntervalRequired(t *testing.T) {
	for _, args := range [][]string{{"summarize"}, {"3x", "summarize"}, {"/git-review-pr"}} {
		m := newTestModel(t)
		m, _ = cmdLoop(m, args)
		if m.activeLoopCount() != 0 || !strings.Contains(m.transcript.String(), "interval required") {
			t.Errorf("cmdLoop(%v) should reject without interval; got %q", args, m.transcript.String())
		}
	}
}

func TestLoop_SubFloorIntervalRejected(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"1s", "do stuff"})
	if m.activeLoopCount() != 0 || !strings.Contains(m.transcript.String(), "floor") {
		t.Fatalf("sub-floor should reject; transcript=%q", m.transcript.String())
	}
}

func TestLoop_EmptyPayloadUsage(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"30s"})
	if m.activeLoopCount() != 0 || !strings.Contains(m.transcript.String(), "usage") {
		t.Fatalf("empty payload should reject; transcript=%q", m.transcript.String())
	}
}

func TestLoop_RefusesDestructivePayloads(t *testing.T) {
	for _, p := range []string{"/quit", "/clear", "/quit now", "/loop 5m x"} {
		m := newTestModel(t)
		m.turnActive = true
		m, _ = cmdLoop(m, append([]string{"30s"}, strings.Fields(p)...))
		if m.activeLoopCount() != 0 {
			t.Errorf("payload %q should be refused", p)
		}
	}
}

func TestLoop_IntervalNoTurnDisarmsOnArm(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"30s", "summarize"})
	if m.activeLoopCount() != 0 || !strings.Contains(m.transcript.String(), "started no turn") {
		t.Fatalf("no-turn prose should disarm; transcript=%q", m.transcript.String())
	}
}

func TestLoop_StopOneAmbiguousAndAll(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	id1 := firstLoop(t, m).id
	m, _ = cmdLoop(m, []string{"45s", "/context"})
	if m.activeLoopCount() != 2 {
		t.Fatal("precondition: two loops")
	}
	m, _ = cmdLoop(m, []string{"stop"})
	if m.activeLoopCount() != 2 || !strings.Contains(m.transcript.String(), "multiple loops active") {
		t.Fatal("bare stop should be ambiguous with multiple loops")
	}
	m, _ = cmdLoop(m, []string{"stop", id1})
	if m.activeLoopCount() != 1 {
		t.Fatalf("stop id should leave one loop; got %d", m.activeLoopCount())
	}
	m, _ = cmdLoop(m, []string{"stop", "all"})
	if m.activeLoopCount() != 0 {
		t.Fatal("stop all should clear loops")
	}
}

func TestLoop_StopWrongIDRefuses(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	m, _ = cmdLoop(m, []string{"stop", "loop-missing"})
	if m.activeLoopCount() != 1 || !strings.Contains(m.transcript.String(), "no active loop") {
		t.Fatal("wrong ID should refuse without stopping")
	}
}

func TestLoop_StaleTickIgnored(t *testing.T) {
	m := newTestModel(t)
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, interval: 30 * time.Second, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(time.Hour)}}
	m.loopOrder = []string{"loop-a"}
	m.disarmLoop("loop-a", "") // removal is what invalidates the pending tick
	before := m.transcript.String()
	m2, cmd := applyMsg(m, loopTickMsg{id: "loop-a"})
	if cmd != nil || m2.transcript.String() != before || m2.activeLoopCount() != 0 {
		t.Fatal("a tick for a disarmed loop should be ignored")
	}
}

func TestLoop_TickReArmsWhileTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, interval: 30 * time.Second, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(time.Hour)}}
	m.loopOrder = []string{"loop-a"}
	m2, cmd := applyMsg(m, loopTickMsg{id: "loop-a"})
	if cmd == nil || m2.activeLoopCount() != 1 {
		t.Fatal("tick during active turn should re-arm")
	}
}

func TestLoop_TickExpiresOneLoop(t *testing.T) {
	m := newTestModel(t)
	m.loops = map[string]loopState{
		"loop-old":  {id: "loop-old", active: true, interval: 30 * time.Second, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(-time.Second)},
		"loop-live": {id: "loop-live", active: true, interval: time.Minute, remaining: -1, payload: "/context", isSlash: true, expiresAt: time.Now().Add(time.Hour)},
	}
	m.loopOrder = []string{"loop-old", "loop-live"}
	m2, _ := applyMsg(m, loopTickMsg{id: "loop-old"})
	if _, ok := m2.loops["loop-old"]; ok || m2.activeLoopCount() != 1 {
		t.Fatalf("expired loop should be removed, got %+v", m2.loops)
	}
}

func TestLoop_CardAndBanner(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, payload: "do it", interval: 5 * time.Minute, remaining: 3, expiresAt: now.Add(loopDefaultTTL)}}
	m.loopOrder = []string{"loop-a"}
	got := stripANSI(renderLoopCard(m.loops["loop-a"], 80))
	for _, want := range []string{"┌ Loop(loop-a)", "every 5m", "3 left", "do it", "/loop stop loop-a", "│", "└"} {
		if !strings.Contains(got, want) {
			t.Errorf("loop card %q missing %q", got, want)
		}
	}
	if strings.ContainsAny(got, "╭╰") {
		t.Fatalf("loop card should use standard card gutters, got %q", got)
	}
	banner := stripANSI(renderLoopBanner(m.loopBannerStates(), 80))
	if !strings.Contains(banner, "loop-a") || !strings.Contains(banner, "every 5m") {
		t.Fatalf("banner = %q", banner)
	}
	m.loops["loop-b"] = loopState{id: "loop-b", active: true, interval: time.Minute, expiresAt: now.Add(time.Hour)}
	m.loopOrder = append(m.loopOrder, "loop-b")
	if multi := stripANSI(renderLoopBanner(m.loopBannerStates(), 80)); !strings.Contains(multi, "2 active") {
		t.Fatalf("multi banner = %q", multi)
	}
}

func TestLoop_CardWrapsLongPayload(t *testing.T) {
	ls := loopState{
		id:        "loop-y7j152",
		active:    true,
		interval:  2 * time.Minute,
		remaining: -1,
		payload:   "Research accountants in Apex and Cary. I need a good accountant for my personal taxes",
		expiresAt: time.Now().Add(loopDefaultTTL),
	}
	card := stripANSI(renderLoopCard(ls, 48))
	rows := strings.Split(card, "\n")
	// Header + meta + >=2 wrapped payload rows + footer.
	if len(rows) < 5 {
		t.Fatalf("expected a multi-row card, got %d rows: %q", len(rows), card)
	}
	for i, row := range rows {
		if w := len([]rune(row)); w > 48 {
			t.Errorf("row %d exceeds width 48 (%d runes): %q", i, w, row)
		}
		// Every row carries a gutter glyph so continuation lines stay inside
		// the card rather than bleeding to column 0.
		if !strings.ContainsAny(string([]rune(row)[0]), "┌│└") {
			t.Errorf("row %d missing gutter: %q", i, row)
		}
	}
	if !strings.HasPrefix(rows[len(rows)-1], "└") {
		t.Errorf("last row should be the footer: %q", rows[len(rows)-1])
	}
}

func TestLoopTurnContext(t *testing.T) {
	unbounded := loopTurnContext(loopState{interval: time.Minute, remaining: -1})
	if !strings.Contains(unbounded, "every 1m") || !strings.Contains(unbounded, "unbounded") {
		t.Errorf("unbounded context = %q", unbounded)
	}
	// Bounded 3x after the first decrement (remaining=2) → iteration 1 of 3.
	bounded := loopTurnContext(loopState{interval: 30 * time.Second, total: 3, remaining: 2})
	if !strings.Contains(bounded, "every 30s") || !strings.Contains(bounded, "iteration 1 of 3") {
		t.Errorf("bounded context = %q", bounded)
	}
}

func TestCompactDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{5 * time.Minute, "5m"},
		{30 * time.Second, "30s"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{2*time.Minute + 30*time.Second, "2m30s"},
		{5 * time.Second, "5s"},
	}
	for _, c := range cases {
		if got := compactDuration(c.in); got != c.want {
			t.Errorf("compactDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoop_EndToEndDispatch(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/loop 30s /help")
	ls := firstLoop(t, m)
	if ls.interval != 30*time.Second || !ls.isSlash || ls.payload != "/help" {
		t.Fatalf("loop state = %+v", ls)
	}
	if !m.helpOpen || !strings.Contains(m.helpPanel, "/help") {
		t.Fatalf("first iteration should execute /help; helpOpen=%v panel=%q", m.helpOpen, m.helpPanel)
	}
}

func TestLoop_ClearAndResumeDisarm(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	m.turnActive = false
	m, _ = cmdClear(m, nil)
	if m.activeLoopCount() != 0 {
		t.Fatal("/clear should disarm loops")
	}
}

func TestLoop_StopWordAsPayload(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "stop", "the", "deploy"})
	ls := firstLoop(t, m)
	if ls.payload != "stop the deploy" {
		t.Fatalf("payload = %q", ls.payload)
	}
}

func TestDispatchSlash_DoesNotRecordHistory(t *testing.T) {
	m := newTestModel(t)
	before := len(m.inputHistory)
	m, _ = m.dispatchSlash("/help")
	if len(m.inputHistory) != before {
		t.Errorf("dispatchSlash must not record input history")
	}
	m, _ = m.runSlash("/help")
	if len(m.inputHistory) != before+1 {
		t.Errorf("runSlash should record once")
	}
}

func TestLoop_UnknownSlashNotArmed(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"5m", "/definitely-not-a-command"})
	if m.activeLoopCount() != 0 || !strings.Contains(m.transcript.String(), "unknown command") {
		t.Fatal("unknown slash payload should not arm")
	}
}

func TestLoop_RegisteredAndReserved(t *testing.T) {
	if findSlash("loop") == nil {
		t.Error("/loop should be registered in allSlash")
	}
	if !usercmd.Reserved["loop"] {
		t.Error(`"loop" should be reserved`)
	}
}

func TestLoop_ExitWarning(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	m.turnActive = false
	out, cmd := requestGracefulExit(m)
	m = out.(Model)
	if cmd != nil || !m.loopExitConfirmOpen {
		t.Fatal("active loops should open exit confirmation")
	}
	view := renderLoopExitConfirm(m)
	if !strings.Contains(view, "Active loops will stop on exit") || !strings.Contains(view, firstLoop(t, m).id) {
		t.Fatalf("exit warning = %q", view)
	}
	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("loop exit confirmation should render as the active popup body")
	}
	first := strings.SplitN(stripANSI(box), "\n", 2)[0]
	if strings.Contains(first, "×") {
		t.Fatalf("loop exit confirmation should not include a mouse-only close glyph, got %q", first)
	}
	out, cmd = m.updateLoopExitConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = out.(Model)
	if m.activeLoopCount() != 0 || cmd == nil {
		t.Fatal("Exit anyway should stop loops and continue graceful exit")
	}
}

// TestLoop_AgentStopViaLoopControl exercises the end-to-end self-stop path: a
// prose loop's iteration marks the turn, the agent calls loop_control{stop}
// (through the real tool), and consumeLoopControl disarms the loop at turn end.
func TestLoop_AgentStopViaLoopControl(t *testing.T) {
	m := newTestModel(t)
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	// Arm an unbounded prose loop. turnActive=true suppresses the immediate
	// fire (newTestModel has no adapter), so the loop just arms.
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"2m", "watch", "CI", "and", "stop", "when", "green"})
	id := firstLoop(t, m).id

	// Simulate the loop's prose turn: fireLoopIteration would set these before
	// the turn goroutine; the model then calls loop_control{stop}.
	m.currentLoopTurnID = id
	lc.SetTurnActive(true)
	if _, err := (&agent.LoopControlTool{State: lc}).Execute(context.Background(), `{"action":"stop","reason":"all checks green"}`); err != nil {
		t.Fatal(err)
	}

	// Turn ends → consume the stop.
	m.turnActive = false
	m.consumeLoopControl()

	if m.activeLoopCount() != 0 {
		t.Fatal("loop_control{stop} should disarm the loop at turn end")
	}
	if lc.IsActive() {
		t.Error("turn-active flag should be cleared at turn end")
	}
	if m.currentLoopTurnID != "" {
		t.Error("loop-turn owner should be cleared at turn end")
	}
	out := m.transcript.String()
	if !strings.Contains(out, "stopped by the agent") || !strings.Contains(out, "all checks green") {
		t.Fatalf("expected agent-stop notice with reason; got %q", out)
	}
}

// TestLoop_ConsumeLoopControlNoStopKeepsLoop confirms a loop iteration that does
// NOT call loop_control keeps looping, and a stray stop with no owning loop is a
// safe no-op.
func TestLoop_ConsumeLoopControlNoStopKeepsLoop(t *testing.T) {
	m := newTestModel(t)
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"2m", "keep", "watching"})
	id := firstLoop(t, m).id

	// Loop turn ran but the agent never asked to stop.
	m.currentLoopTurnID = id
	lc.SetTurnActive(true)
	m.turnActive = false
	m.consumeLoopControl()
	if m.activeLoopCount() != 1 {
		t.Fatal("no stop request → loop must keep running")
	}
	if lc.IsActive() {
		t.Error("turn-active flag should still be cleared")
	}

	// A stop request with no owning loop (currentLoopTurnID cleared) is a no-op.
	lc.SetTurnActive(true)
	(&agent.LoopControlTool{State: lc}).Execute(context.Background(), `{"action":"stop"}`)
	m.consumeLoopControl()
	if m.activeLoopCount() != 1 {
		t.Fatal("stop with no owning loop-turn must not disarm anything")
	}
}

// TestLoop_FireIterationMarksTurnActive verifies the real fireLoopIteration
// wiring: when a prose iteration actually starts a turn, it records the owning
// loop and flips loop_control on so the agent can see the tool.
func TestLoop_FireIterationMarksTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{} // lets the prose turn start
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	// Idle: arming immediately fires iteration 1, which starts the prose turn.
	m, _ = cmdLoop(m, []string{"2m", "watch", "CI"})
	id := firstLoop(t, m).id
	if !m.turnActive {
		t.Fatal("prose iteration should have started a turn")
	}
	if m.currentLoopTurnID != id {
		t.Fatalf("currentLoopTurnID = %q, want %q", m.currentLoopTurnID, id)
	}
	if !lc.IsActive() {
		t.Error("loop_control should be advertised during the loop's prose turn")
	}
}

// TestLoop_FireIterationUndoesActiveWhenNoTurn verifies the undo half: when the
// prose turn can't start (no provider), fireLoopIteration must not leave the
// loop_control flag stuck on or record a phantom owner.
func TestLoop_FireIterationUndoesActiveWhenNoTurn(t *testing.T) {
	m := newTestModel(t) // newTestModel has no adapter
	lc := &agent.LoopControlState{}
	m.cfg.LoopControl = lc
	m, _ = cmdLoop(m, []string{"2m", "do", "stuff"})
	if lc.IsActive() {
		t.Error("a no-turn prose iteration must not leave loop_control active")
	}
	if m.currentLoopTurnID != "" {
		t.Error("a no-turn prose iteration must not record a loop-turn owner")
	}
	if m.activeLoopCount() != 0 {
		t.Error("a no-turn prose loop should have disarmed")
	}
}

// TestLoop_StopOnlyCancelsOwningTurn is the review-#1 fix: stopping one loop
// must cancel the in-flight turn only when that loop owns it — never a different
// loop's (or a user's) running turn.
func TestLoop_StopOnlyCancelsOwningTurn(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true // suppress immediate fire while arming
	m, _ = cmdLoop(m, []string{"30s", "watch", "A"})
	idA := firstLoop(t, m).id
	m, _ = cmdLoop(m, []string{"1m", "watch", "B"})
	var idB string
	for _, id := range m.activeLoopIDs() {
		if id != idA {
			idB = id
		}
	}
	if idB == "" {
		t.Fatal("precondition: two distinct loops")
	}

	// loopA's prose iteration owns the current turn.
	cancelled := false
	m.currentLoopTurnID = idA
	m.turnActive = true
	m.turnCancel = func() { cancelled = true }

	// Stopping the OTHER loop disarms it but must NOT cancel loopA's turn.
	m, _ = cmdLoop(m, []string{"stop", idB})
	if cancelled {
		t.Fatal("stopping a non-owning loop must not cancel the in-flight turn")
	}
	if m.activeLoopCount() != 1 {
		t.Fatalf("loopB should be stopped; count=%d", m.activeLoopCount())
	}

	// Stopping the owning loop cancels its turn.
	m, _ = cmdLoop(m, []string{"stop", idA})
	if !cancelled {
		t.Fatal("stopping the owning loop should cancel its in-flight turn")
	}
}

// TestLoop_StopUnrelatedThenUserTurn covers the user-turn case: a single loop is
// armed but a user-initiated turn is running (the loop owns nothing). `/loop
// stop` must leave that turn alone.
func TestLoop_StopDoesNotCancelUserTurn(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "watch", "CI"})
	// A user turn is running; the loop is between ticks and owns no turn.
	cancelled := false
	m.currentLoopTurnID = "" // no loop owns the turn
	m.turnActive = true
	m.turnCancel = func() { cancelled = true }
	m, _ = cmdLoop(m, []string{"stop"}) // exactly one loop → bare stop
	if cancelled {
		t.Fatal("bare /loop stop must not cancel a user-initiated turn the loop doesn't own")
	}
	if m.activeLoopCount() != 0 {
		t.Fatal("the loop should still be disarmed")
	}
}
