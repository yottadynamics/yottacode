package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

func TestParseLoopInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"5m", 5 * time.Minute, true},
		{"30s", 30 * time.Second, true},
		{"1h", time.Hour, true},
		{"check", 0, false}, // prose falls through to payload
		{"3x", 0, false},    // count token, not a duration
		{"0s", 0, false},    // non-positive rejected
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
		{"3x", 3, true},
		{"10X", 10, true},
		{"1x", 1, true},
		{"x", 0, false},
		{"0x", 0, false},
		{"-2x", 0, false},
		{"5m", 0, false},
		{"abc", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseLoopCount(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLoopCount(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Arming an interval loop parses interval + count + payload and schedules
// a tick. turnActive is forced on so the immediate first fire is
// suppressed, letting us inspect the armed state cleanly.
func TestLoop_ArmsIntervalState(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, cmd := cmdLoop(m, []string{"30s", "3x", "summarize", "open", "work"})
	if !m.loop.active {
		t.Fatal("expected loop armed")
	}
	if m.loop.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", m.loop.interval)
	}
	if m.loop.remaining != 3 {
		t.Errorf("remaining = %d, want 3 (no immediate fire while turnActive)", m.loop.remaining)
	}
	if m.loop.payload != "summarize open work" {
		t.Errorf("payload = %q", m.loop.payload)
	}
	if m.loop.isSlash {
		t.Error("prose payload should not be flagged as slash")
	}
	if cmd == nil {
		t.Error("arming an interval loop should schedule a tick cmd")
	}
}

// A self-paced arm (no interval) with a slash payload sets isSlash so the
// iteration routes through runSlash rather than being sent verbatim.
func TestLoop_SlashPayloadRouted(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"/git-review-pr"})
	if !m.loop.active {
		t.Fatal("expected loop armed")
	}
	if !m.loop.isSlash {
		t.Error("slash payload should set isSlash")
	}
	if m.loop.interval != 0 {
		t.Errorf("self-paced interval should be 0; got %v", m.loop.interval)
	}
	if m.loop.payload != "/git-review-pr" {
		t.Errorf("payload = %q", m.loop.payload)
	}
}

func TestLoop_SubFloorIntervalRejected(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"1s", "do stuff"})
	if m.loop.active {
		t.Error("sub-floor interval should not arm a loop")
	}
	if !strings.Contains(m.transcript.String(), "floor") {
		t.Errorf("should explain the interval floor; got %q", m.transcript.String())
	}
}

func TestLoop_EmptyPayloadUsage(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"30s"})
	if m.loop.active {
		t.Error("interval with no payload should not arm")
	}
	if !strings.Contains(m.transcript.String(), "usage") {
		t.Errorf("empty payload should print usage; got %q", m.transcript.String())
	}
}

func TestLoop_SelfReferentialRejected(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/loop", "5m", "foo"})
	if m.loop.active {
		t.Error("a /loop payload should be refused, not armed")
	}
	if !strings.Contains(m.transcript.String(), "another /loop") {
		t.Errorf("should explain the refusal; got %q", m.transcript.String())
	}
}

// A self-paced loop whose payload starts no turn (no adapter in the test
// model, so prose can't start one) must disarm rather than spin.
func TestLoop_SelfPacedNoTurnDisarms(t *testing.T) {
	m := newTestModel(t) // LoopConfig has no Adapter
	m, _ = cmdLoop(m, []string{"summarize"})
	if m.loop.active {
		t.Error("self-paced payload that started no turn should disarm")
	}
	if !strings.Contains(m.transcript.String(), "started no turn") {
		t.Errorf("should explain the disarm; got %q", m.transcript.String())
	}
}

func TestLoop_Stop(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "keep going"})
	if !m.loop.active {
		t.Fatal("precondition: loop should be armed")
	}
	genBefore := m.loop.gen
	m, _ = cmdLoop(m, []string{"stop"})
	if m.loop.active {
		t.Error("/loop stop should disarm")
	}
	if m.loop.gen == genBefore {
		t.Error("/loop stop should bump gen so a stale tick is dropped")
	}
	if !strings.Contains(m.transcript.String(), "stopped") {
		t.Errorf("stop should confirm; got %q", m.transcript.String())
	}
}

func TestLoop_StopWhenNoneArmed(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"stop"})
	if !strings.Contains(m.transcript.String(), "nothing to stop") {
		t.Errorf("stop with no loop should say so; got %q", m.transcript.String())
	}
}

// disarmLoop is a no-op (no gen bump, nothing printed) when nothing is
// armed — so the Esc/Ctrl+C hooks don't spam a notice on every keypress.
func TestLoop_DisarmNoopWhenInactive(t *testing.T) {
	m := newTestModel(t)
	genBefore := m.loop.gen
	before := m.transcript.String()
	m.disarmLoop("[loop] stopped")
	if m.loop.gen != genBefore {
		t.Error("disarm on an inactive loop should not bump gen")
	}
	if m.transcript.String() != before {
		t.Error("disarm on an inactive loop should print nothing")
	}
}

// A tick stamped with a stale generation (from a stopped/replaced loop)
// is dropped: no iteration fires and no cmd is returned. Loop state is set
// directly (not via cmdLoop) so no queued appendLine flush rides along on
// the Update and muddies the cmd assertion.
func TestLoop_StaleTickIgnored(t *testing.T) {
	m := newTestModel(t)
	m.loop = loopState{active: true, interval: 30 * time.Second, gen: 5, remaining: -1, payload: "/git-review-pr", isSlash: true}
	before := m.transcript.String()
	m2, cmd := applyMsg(m, loopTickMsg{gen: 4}) // stale: current gen is 5
	if cmd != nil {
		t.Error("a stale tick should produce no cmd")
	}
	if m2.transcript.String() != before {
		t.Error("a stale tick should not fire an iteration")
	}
	if m2.loop.remaining != -1 {
		t.Errorf("stale tick should not touch loop state; remaining = %d", m2.loop.remaining)
	}
}

// A current-generation tick that lands while a turn is active skips the
// iteration but re-arms the next interval (returns a non-nil cmd).
func TestLoop_TickReArmsWhileTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.loop = loopState{active: true, interval: 30 * time.Second, gen: 5, remaining: -1, payload: "/git-review-pr", isSlash: true}
	before := m.transcript.String()
	m2, cmd := applyMsg(m, loopTickMsg{gen: 5})
	if cmd == nil {
		t.Error("a tick during an active turn should re-arm (non-nil cmd)")
	}
	if m2.transcript.String() != before {
		t.Error("a tick during an active turn should not fire an iteration")
	}
	if m2.loop.remaining != -1 {
		t.Errorf("unbounded loop remaining should stay -1; got %d", m2.loop.remaining)
	}
}

func TestLoop_StatusLine(t *testing.T) {
	m := newTestModel(t)
	m.loop = loopState{active: true, payload: "do it", interval: 5 * time.Minute, remaining: 3}
	got := m.loopStatusLine()
	for _, want := range []string{"every 5m0s", "3 left", `"do it"`, "/loop stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line %q missing %q", got, want)
		}
	}
	m.loop = loopState{active: true, payload: "poll", interval: 0, remaining: -1}
	if got := m.loopStatusLine(); !strings.Contains(got, "self-paced") || !strings.Contains(got, "unbounded") {
		t.Errorf("self-paced status line = %q", got)
	}
}

// End-to-end through the real keypress → dispatch → runSlash → cmdLoop
// path (not a direct cmdLoop call): typing the command arms the loop and
// its first iteration executes the slash payload.
func TestLoop_EndToEndDispatch(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/loop 30s /help")
	if !m.loop.active {
		t.Fatal("/loop via the dispatcher should arm a loop")
	}
	if m.loop.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", m.loop.interval)
	}
	if !m.loop.isSlash || m.loop.payload != "/help" {
		t.Errorf("payload routing: isSlash=%v payload=%q", m.loop.isSlash, m.loop.payload)
	}
	// The immediate first iteration ran /help, so its output is in scrollback.
	if !strings.Contains(m.transcript.String(), "Available commands") {
		t.Errorf("first iteration should have executed /help; transcript=%q", m.transcript.String())
	}
}

// /clear must disarm an armed loop so it can't bleed into the fresh
// session (its arm line was wiped, so a silent survivor is invisible).
func TestLoop_ClearDisarms(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "keep going"})
	if !m.loop.active {
		t.Fatal("precondition: loop should be armed")
	}
	m.turnActive = false // /clear can't run mid-turn (it cancels first)
	m, _ = cmdClear(m, nil)
	if m.loop.active {
		t.Error("/clear should disarm the loop")
	}
}

// Resuming/switching sessions must disarm a loop armed against the prior
// conversation.
func TestLoop_ResumeDisarms(t *testing.T) {
	m := newTestModel(t)
	other, _ := session.New("test-model", "/cwd")
	other.Name = "elsewhere"
	if err := other.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "keep going"})
	if !m.loop.active {
		t.Fatal("precondition: loop should be armed")
	}
	m.turnActive = false
	m, _ = m.resumeSession(other.ID, false)
	if m.loop.active {
		t.Error("resuming a session should disarm the loop")
	}
}

func TestLoop_RefusesDestructivePayloads(t *testing.T) {
	for _, p := range []string{"/quit", "/clear", "/quit now", "/loop 5m x"} {
		m := newTestModel(t)
		m.turnActive = true // suppress the immediate fire; we only check arming
		m, _ = cmdLoop(m, append([]string{"30s"}, strings.Fields(p)...))
		if m.loop.active {
			t.Errorf("payload %q should be refused, not armed", p)
		}
	}
}

// A bounded loop prints a K/N progress line each iteration.
func TestLoop_BoundedProgressLine(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"30s", "3x", "/help"})
	if m.loop.total != 3 {
		t.Errorf("total = %d, want 3", m.loop.total)
	}
	if m.loop.remaining != 2 {
		t.Errorf("remaining after first fire = %d, want 2", m.loop.remaining)
	}
	if !strings.Contains(m.transcript.String(), "iteration 1/3") {
		t.Errorf("bounded loop should print a progress line; transcript=%q", m.transcript.String())
	}
}

func TestLoop_BannerRender(t *testing.T) {
	got := stripANSI(renderLoopBanner(loopState{active: true, interval: 5 * time.Minute, remaining: 2, total: 3}, 80))
	for _, want := range []string{"loop", "every 5m", "2 left", "/loop stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner %q missing %q", got, want)
		}
	}
	sp := stripANSI(renderLoopBanner(loopState{active: true, interval: 0, remaining: -1}, 80))
	if !strings.Contains(sp, "self-paced") {
		t.Errorf("self-paced banner = %q", sp)
	}
}

// An armed loop surfaces its banner in the rendered View so it stays
// visible after the arm line scrolls away.
func TestLoop_BannerShowsInView(t *testing.T) {
	m := newTestModel(t)
	m, _ = cmdLoop(m, []string{"5m", "poll status"}) // interval keeps it armed
	if !m.loop.active {
		t.Fatal("precondition: interval loop should stay armed")
	}
	v := stripANSI(m.View())
	if !strings.Contains(v, "loop") || !strings.Contains(v, "every 5m") {
		t.Errorf("armed loop should render a banner in View; got:\n%s", v)
	}
}

// Fix #1: the shared self-paced re-fire helper (used by both turnEndedMsg
// and summaryDoneMsg) fires only self-paced, idle, non-summarizing loops.
func TestLoop_RefireSelfPacedHelper(t *testing.T) {
	// interval loop → no-op (timer-driven, not helper-driven)
	m := newTestModel(t)
	m.loop = loopState{active: true, interval: 30 * time.Second, gen: 1, remaining: -1}
	if _, _, fired := m.refireSelfPacedLoop(); fired {
		t.Error("interval loop must not be re-fired by the self-paced helper")
	}
	// summarizing → no-op (never overlap compaction)
	m2 := newTestModel(t)
	m2.loop = loopState{active: true, interval: 0, gen: 1, remaining: -1, payload: "poll"}
	m2.summarizing = true
	if _, _, fired := m2.refireSelfPacedLoop(); fired {
		t.Error("must not re-fire while summarizing")
	}
	// self-paced idle → fires; with no adapter the iteration starts no turn,
	// so it disarms rather than spin.
	m3 := newTestModel(t)
	m3.loop = loopState{active: true, interval: 0, gen: 1, remaining: -1, payload: "poll"}
	got, _, fired := m3.refireSelfPacedLoop()
	if !fired {
		t.Error("self-paced idle loop should re-fire")
	}
	if got.loop.active {
		t.Error("a re-fire that starts no turn should disarm (no adapter in test)")
	}
}

// Fix #2: only a bare `/loop stop` disarms; a prose payload starting with
// "stop" is armed, not swallowed.
func TestLoop_StopWordAsPayload(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "stop", "the", "deploy"})
	if !m.loop.active {
		t.Fatal("`/loop 30s stop the deploy` should arm a prose loop, not disarm")
	}
	if m.loop.payload != "stop the deploy" {
		t.Errorf("payload = %q, want 'stop the deploy'", m.loop.payload)
	}
	m, _ = cmdLoop(m, []string{"stop"}) // bare stop still disarms
	if m.loop.active {
		t.Error("bare `/loop stop` should disarm")
	}
}

// Fix #4: loop iterations dispatch via dispatchSlash, which does NOT record
// input history (unlike the user-typed runSlash path).
func TestDispatchSlash_DoesNotRecordHistory(t *testing.T) {
	m := newTestModel(t)
	before := len(m.inputHistory)
	m, _ = m.dispatchSlash("/help")
	if len(m.inputHistory) != before {
		t.Errorf("dispatchSlash must not record input history; grew by %d", len(m.inputHistory)-before)
	}
	m, _ = m.runSlash("/help") // the user-typed path still records
	if len(m.inputHistory) != before+1 {
		t.Errorf("runSlash should record once; grew by %d", len(m.inputHistory)-before)
	}
}

// Fix #6: a /loop with an unknown slash payload is refused at arm time
// rather than looping "unknown command" forever.
func TestLoop_UnknownSlashNotArmed(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"5m", "/definitely-not-a-command"})
	if m.loop.active {
		t.Error("a /loop with an unknown slash payload should not arm")
	}
	if !strings.Contains(m.transcript.String(), "unknown command") {
		t.Errorf("should explain the unknown command; got %q", m.transcript.String())
	}
}

func TestLoop_RegisteredAndReserved(t *testing.T) {
	if findSlash("loop") == nil {
		t.Error("/loop should be registered in allSlash")
	}
	if !usercmd.Reserved["loop"] {
		t.Error(`"loop" should be in usercmd.Reserved so a custom command can't shadow it`)
	}
}
