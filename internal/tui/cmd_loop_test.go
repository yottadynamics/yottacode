package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	m, _ = cmdLoop(m, nil)
	out := m.transcript.String()
	if !strings.Contains(out, ids[0]) || !strings.Contains(out, ids[1]) {
		t.Fatalf("status should include both IDs; got %q", out)
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
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, interval: 30 * time.Second, gen: 5, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(time.Hour)}}
	m.loopOrder = []string{"loop-a"}
	before := m.transcript.String()
	m2, cmd := applyMsg(m, loopTickMsg{id: "loop-a", gen: 4})
	if cmd != nil || m2.transcript.String() != before || m2.activeLoopCount() != 1 {
		t.Fatal("stale tick should be ignored")
	}
}

func TestLoop_TickReArmsWhileTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, interval: 30 * time.Second, gen: 5, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(time.Hour)}}
	m.loopOrder = []string{"loop-a"}
	m2, cmd := applyMsg(m, loopTickMsg{id: "loop-a", gen: 5})
	if cmd == nil || m2.activeLoopCount() != 1 {
		t.Fatal("tick during active turn should re-arm")
	}
}

func TestLoop_TickExpiresOneLoop(t *testing.T) {
	m := newTestModel(t)
	m.loops = map[string]loopState{
		"loop-old":  {id: "loop-old", active: true, interval: 30 * time.Second, gen: 1, remaining: -1, payload: "/help", isSlash: true, expiresAt: time.Now().Add(-time.Second)},
		"loop-live": {id: "loop-live", active: true, interval: time.Minute, gen: 1, remaining: -1, payload: "/context", isSlash: true, expiresAt: time.Now().Add(time.Hour)},
	}
	m.loopOrder = []string{"loop-old", "loop-live"}
	m2, _ := applyMsg(m, loopTickMsg{id: "loop-old", gen: 1})
	if _, ok := m2.loops["loop-old"]; ok || m2.activeLoopCount() != 1 {
		t.Fatalf("expired loop should be removed, got %+v", m2.loops)
	}
}

func TestLoop_StatusLineAndBanner(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	m.loops = map[string]loopState{"loop-a": {id: "loop-a", active: true, payload: "do it", interval: 5 * time.Minute, remaining: 3, expiresAt: now.Add(loopDefaultTTL)}}
	m.loopOrder = []string{"loop-a"}
	got := m.loopStatusLine("loop-a")
	for _, want := range []string{"loop-a", "every 5m0s", "3 left", `"do it"`, "/loop stop loop-a"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line %q missing %q", got, want)
		}
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

func TestLoop_EndToEndDispatch(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/loop 30s /help")
	ls := firstLoop(t, m)
	if ls.interval != 30*time.Second || !ls.isSlash || ls.payload != "/help" {
		t.Fatalf("loop state = %+v", ls)
	}
	if !strings.Contains(m.transcript.String(), "Available commands") {
		t.Fatalf("first iteration should execute /help; transcript=%q", m.transcript.String())
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
	if !strings.Contains(view, "Background work is running") || !strings.Contains(view, firstLoop(t, m).id) {
		t.Fatalf("exit warning = %q", view)
	}
	out, cmd = m.updateLoopExitConfirm(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.activeLoopCount() != 0 || cmd == nil {
		t.Fatal("Exit anyway should stop loops and continue graceful exit")
	}
}
