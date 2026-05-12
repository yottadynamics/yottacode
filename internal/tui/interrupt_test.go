package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// installFakeTurn arms the model as if a turn were in flight. It plants a
// turnCancel that just records invocations on the returned counter so
// tests can assert that mid-turn key handlers invoked cancellation
// without spinning up a real agent goroutine.
func installFakeTurn(t *testing.T, m *Model) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	m.turnActive = true
	m.turnCancel = func() { calls.Add(1) }
	return &calls
}

// TestInterrupt_EnterMidTurnQueuesInputAndCancels guards the
// interrupt-with-feedback flow: typing a non-empty message and
// pressing Enter while the agent is working must (1) capture the
// input for resubmission, (2) fire turnCancel so the agent loop
// stops, and (3) clear the textarea so the queued message doesn't
// appear stale in the input row.
func TestInterrupt_EnterMidTurnQueuesInputAndCancels(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "follow up" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.pendingInputAfterTurn; got != "follow up" {
		t.Errorf("pendingInputAfterTurn = %q, want %q", got, "follow up")
	}
	if got := cancels.Load(); got != 1 {
		t.Errorf("turnCancel call count = %d, want 1", got)
	}
	if got := m.textInput.Value(); got != "" {
		t.Errorf("textInput.Value = %q, want empty (textarea cleared on queue)", got)
	}
	if !m.turnActive {
		t.Errorf("turnActive should remain true until turnEndedMsg lands")
	}
}

// TestInterrupt_EmptyEnterMidTurnIsSilent confirms that an Enter with
// nothing in the textarea while a turn is in flight is a no-op — we
// don't want to cancel-without-input on a stray Enter press. Ctrl+C
// and Esc are the explicit "stop without sending" surface.
func TestInterrupt_EmptyEnterMidTurnIsSilent(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should stay empty on bare Enter; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got != 0 {
		t.Errorf("bare Enter must not cancel the turn; cancel count = %d", got)
	}
	if !m.turnActive {
		t.Errorf("turnActive should remain true")
	}
}

// TestInterrupt_CtrlCMidTurnCancelsAndDropsQueue covers the explicit
// stop-without-sending path. If the user had already queued a message
// via Enter, Ctrl+C must drop that queue so turnEndedMsg doesn't
// auto-submit it after the cancel unwinds. Without this, Ctrl+C would
// silently fire the queued message — surprising behaviour given the
// key is meant to stop work.
func TestInterrupt_CtrlCMidTurnCancelsAndDropsQueue(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "abc" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingInputAfterTurn != "abc" {
		t.Fatalf("setup: pendingInputAfterTurn = %q, want %q", m.pendingInputAfterTurn, "abc")
	}

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("Ctrl+C should drop queued message; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got < 2 {
		t.Errorf("Ctrl+C must call turnCancel (cumulative count after Enter+Ctrl+C = %d, want >=2)", got)
	}
}

// TestInterrupt_EscBehavesLikeCtrlC verifies the Esc binding added in
// this pass mirrors Ctrl+C. Mirrors Claude Code's "Esc cancels" feel
// for users who reach for Esc by reflex.
func TestInterrupt_EscBehavesLikeCtrlC(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "xyz" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("Esc should drop queued message; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got < 2 {
		t.Errorf("Esc must call turnCancel; cumulative count = %d", got)
	}
}

// TestInterrupt_TurnEndedAutoSubmitsQueued covers the second half of
// the flow: after turnCancel resolves and turnEndedMsg arrives, the
// model must consume pendingInputAfterTurn and kick off a fresh turn
// with the queued message. The adapter is nil in newTestModel so the
// follow-up startTurn fails fast with a "no provider" line — what we
// assert is that the queue was consumed and turnActive flipped, not
// that a real model call fired.
func TestInterrupt_TurnEndedAutoSubmitsQueued(t *testing.T) {
	m := newTestModel(t)
	_ = installFakeTurn(t, &m)

	for _, r := range "do the thing" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingInputAfterTurn != "do the thing" {
		t.Fatalf("setup: pendingInputAfterTurn = %q", m.pendingInputAfterTurn)
	}

	m, _ = applyMsg(m, turnEndedMsg{})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("turnEnded must consume the queue; pendingInputAfterTurn = %q", m.pendingInputAfterTurn)
	}
	// newTestModel intentionally leaves Adapter unset, so startTurn
	// fast-fails at its no-provider guard before rendering the user
	// block. The presence of that guard line proves startTurn was
	// invoked — i.e. the queue triggered a real auto-submit, not a
	// silent drop. (Tests that need to verify the user message
	// rendering itself would need to wire a stub Adapter.)
	if !strings.Contains(m.transcript.String(), "no provider configured") {
		t.Errorf("expected startTurn to fire after queue auto-submit; transcript=%q", m.transcript.String())
	}
}

// TestInterrupt_TurnEndedWithoutQueueLeavesNoTrace is the negative
// case: a normal turn that completed without a mid-turn Enter should
// not surface any queued-input artifacts. Guards against accidental
// regression where pendingInputAfterTurn gains a default value.
func TestInterrupt_TurnEndedWithoutQueueLeavesNoTrace(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnCancel = func() {}
	pre := m.transcript.String()

	m, _ = applyMsg(m, turnEndedMsg{})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should stay empty; got %q", m.pendingInputAfterTurn)
	}
	if m.turnActive {
		t.Errorf("turnActive should flip to false on turnEnded")
	}
	if got := m.transcript.String(); got != pre {
		t.Errorf("transcript should be unchanged without a queue; got new content %q", strings.TrimPrefix(got, pre))
	}
}

// TestInterrupt_SlashMidTurnClearsQueue guards the context-wipe race:
// after Enter queues a follow-up, the user might immediately type a
// /clear or other context-shifting slash command. The slash branch
// must discard the queue so we don't auto-submit the stale message
// into a wiped session after turnEndedMsg arrives.
func TestInterrupt_SlashMidTurnClearsQueue(t *testing.T) {
	m := newTestModel(t)
	_ = installFakeTurn(t, &m)

	for _, r := range "stale message" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingInputAfterTurn != "stale message" {
		t.Fatalf("setup: pendingInputAfterTurn = %q", m.pendingInputAfterTurn)
	}

	// Type a /clear and press Enter. runSlash will execute since the
	// /clear command exists, but the assertion we care about is
	// pre-runSlash: the queue must be dropped in the slash branch.
	for _, r := range "/clear" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("mid-turn slash must drop queued message; got %q", m.pendingInputAfterTurn)
	}
}

