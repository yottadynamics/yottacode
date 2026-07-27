package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// installFakeTurn arms the model as if a turn were in flight. It plants a
// turnCancel that just records invocations on the returned counter so
// tests can assert that mid-turn key handlers invoked cancellation
// without spinning up a real agent goroutine. Also creates the
// userMsgCh channel the append path needs.
func installFakeTurn(t *testing.T, m *Model) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	m.turnActive = true
	m.turnCancel = func() { calls.Add(1) }
	m.userMsgCh = make(chan string, 1)
	return &calls
}

// TestInterrupt_EnterMidTurnQueuesOnChannel guards the append-at-next-
// tool-round flow: typing a non-empty message and pressing Enter while
// the agent is working must (1) send the input on userMsgCh for the
// agent loop to pick up between tool rounds, (2) NOT cancel the turn,
// and (3) clear the textarea.
func TestInterrupt_EnterMidTurnQueuesOnChannel(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "follow up" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Message should be on the channel, not in pendingInputAfterTurn.
	select {
	case got := <-m.userMsgCh:
		if got != "follow up" {
			t.Errorf("userMsgCh message = %q, want %q", got, "follow up")
		}
	default:
		t.Errorf("userMsgCh should have the queued message")
	}
	if got := cancels.Load(); got != 0 {
		t.Errorf("turnCancel should NOT be called (append path); got %d calls", got)
	}
	if got := m.textInput.Value(); got != "" {
		t.Errorf("textInput.Value = %q, want empty", got)
	}
	if !m.turnActive {
		t.Errorf("turnActive should remain true")
	}
	plain := stripANSI(m.transcript.String())
	if !strings.Contains(plain, "\n\n→ queued: will deliver next tool round · follow up\n\n") {
		t.Fatalf("queued notice should be a spaced one-line system row; transcript=%q", plain)
	}
}

// TestInterrupt_UpMidTurnRecallsQueuedMessageForEditing covers the edit-
// before-delivery path: after a mid-turn Enter queues a message, Up pulls it
// back into the textarea and removes it from the delivery channel.
func TestInterrupt_UpMidTurnRecallsQueuedMessageForEditing(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "follow up" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})

	if got := m.textInput.Value(); got != "follow up" {
		t.Fatalf("Up should recall queued text into textarea; got %q", got)
	}
	select {
	case got := <-m.userMsgCh:
		t.Fatalf("Up should drain queued delivery; still had %q", got)
	default:
	}
	if got := cancels.Load(); got != 0 {
		t.Fatalf("recalling queued text must not cancel turn; got %d calls", got)
	}
	if !strings.Contains(m.transcript.String(), "→ queued: recalled for editing") {
		t.Fatalf("expected recall notice in transcript; got %q", m.transcript.String())
	}
}

// TestInterrupt_UpMidTurnCanReviseAndRequeueQueuedMessage verifies that the
// recalled text can be edited and submitted again through the same queue path.
func TestInterrupt_UpMidTurnCanReviseAndRequeueQueuedMessage(t *testing.T) {
	m := newTestModel(t)
	_ = installFakeTurn(t, &m)

	for _, r := range "follow" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	m.textInput.SetValue("follow up revised")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case got := <-m.userMsgCh:
		if got != "follow up revised" {
			t.Fatalf("requeued message = %q, want revised text", got)
		}
	default:
		t.Fatalf("revised message should be queued")
	}
	if got := m.textInput.Value(); got != "" {
		t.Fatalf("textarea should clear after requeue; got %q", got)
	}
}

// TestInterrupt_EnterMidTurnOverflowDoesNotCancel verifies that a second
// mid-turn Enter before the first queued message is consumed does not cancel
// the active turn. Normal typing must not interrupt in-flight tool calls.
func TestInterrupt_EnterMidTurnOverflowDoesNotCancel(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	// First message: queued on channel.
	for _, r := range "first" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Second message: channel full, stays in the textarea and only warns.
	for _, r := range "second" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case got := <-m.userMsgCh:
		if got != "first" {
			t.Fatalf("first queued message changed: got %q", got)
		}
	default:
		t.Fatalf("first queued message should remain pending")
	}
	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should stay empty; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got != 0 {
		t.Errorf("turnCancel should not fire on queue overflow; got %d", got)
	}
	if got := m.textInput.Value(); got != "second" {
		t.Errorf("second message should remain editable in textarea; got %q", got)
	}
	if !strings.Contains(stripANSI(m.transcript.String()), "already waiting for delivery") {
		t.Fatalf("expected queue-full warning; transcript=%q", stripANSI(m.transcript.String()))
	}
}

// TestInterrupt_EmptyEnterMidTurnIsSilent confirms that an Enter with
// nothing in the textarea while a turn is in flight is a no-op.
func TestInterrupt_EmptyEnterMidTurnIsSilent(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case msg := <-m.userMsgCh:
		t.Errorf("userMsgCh should be empty on bare Enter; got %q", msg)
	default:
	}
	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should stay empty; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got != 0 {
		t.Errorf("bare Enter must not cancel; cancel count = %d", got)
	}
}

// TestInterrupt_CtrlCMidTurnCancelsAndDrainsChannel covers the explicit
// stop path. Ctrl+C must cancel the turn AND drain any message queued
// on userMsgCh so turnEndedMsg doesn't auto-submit it.
func TestInterrupt_CtrlCMidTurnCancelsAndDrainsChannel(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "abc" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Verify message was queued (not cancelled yet).
	// Don't drain it — let Ctrl+C do that.

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})

	select {
	case msg := <-m.userMsgCh:
		t.Errorf("Ctrl+C should drain userMsgCh; got %q", msg)
	default:
	}
	if m.pendingInputAfterTurn != "" {
		t.Errorf("Ctrl+C should clear pendingInputAfterTurn; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got < 1 {
		t.Errorf("Ctrl+C must call turnCancel; count = %d", got)
	}
}

// TestInterrupt_EscBehavesLikeCtrlC verifies Esc mirrors Ctrl+C.
func TestInterrupt_EscBehavesLikeCtrlC(t *testing.T) {
	m := newTestModel(t)
	cancels := installFakeTurn(t, &m)

	for _, r := range "xyz" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	select {
	case msg := <-m.userMsgCh:
		t.Errorf("Esc should drain userMsgCh; got %q", msg)
	default:
	}
	if m.pendingInputAfterTurn != "" {
		t.Errorf("Esc should clear pendingInputAfterTurn; got %q", m.pendingInputAfterTurn)
	}
	if got := cancels.Load(); got < 1 {
		t.Errorf("Esc must call turnCancel; count = %d", got)
	}
}

func TestInterrupt_UserMessageAppendedRendersSpacedDeliveredNotice(t *testing.T) {
	m := newTestModel(t)

	out, _ := m.handleAgentEvent(agent.UserMessageAppended{Content: "follow up delivered"})
	m = out.(Model)

	plain := stripANSI(m.transcript.String())
	if !strings.Contains(plain, "\n\n✓ delivered: follow up delivered\n\n") {
		t.Fatalf("delivered notice should be a spaced one-line system row; transcript=%q", plain)
	}
}

// TestInterrupt_TurnEndedDrainsUndeliveredMessage covers the case where
// the user queued a message but the model finished without a tool round
// (no injection point). The undelivered message should be drained from
// userMsgCh and auto-submitted as a new turn.
func TestInterrupt_TurnEndedDrainsUndeliveredMessage(t *testing.T) {
	m := newTestModel(t)
	_ = installFakeTurn(t, &m)

	for _, r := range "do the thing" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	m, _ = applyMsg(m, turnEndedMsg{})

	// The undelivered message should have been auto-submitted.
	if m.pendingInputAfterTurn != "" {
		t.Errorf("turnEnded must consume the queue; pendingInputAfterTurn = %q", m.pendingInputAfterTurn)
	}
	if !strings.Contains(m.transcript.String(), "no provider configured") {
		t.Errorf("expected startTurn to fire after queue auto-submit; transcript=%q", m.transcript.String())
	}
}

// TestInterrupt_TurnEndedWithoutQueueLeavesNoTrace is the negative case:
// a normal turn that completed without a mid-turn Enter should not
// surface any queued-input artifacts.
func TestInterrupt_TurnEndedWithoutQueueLeavesNoTrace(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnCancel = func() {}
	m.userMsgCh = make(chan string, 1)
	pre := m.transcript.String()

	m, _ = applyMsg(m, turnEndedMsg{})

	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should stay empty; got %q", m.pendingInputAfterTurn)
	}
	if m.turnActive {
		t.Errorf("turnActive should flip to false")
	}
	if got := m.transcript.String(); got != pre {
		t.Errorf("transcript should be unchanged; got new content %q", strings.TrimPrefix(got, pre))
	}
}

// TestInterrupt_SlashMidTurnDrainsChannel guards the context-wipe race:
// after Enter queues a message on userMsgCh, a /clear should drain the
// channel so the stale message doesn't auto-submit after turnEndedMsg.
func TestInterrupt_SlashMidTurnDrainsChannel(t *testing.T) {
	m := newTestModel(t)
	_ = installFakeTurn(t, &m)

	for _, r := range "stale message" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Type /clear and press Enter.
	m.textInput.SetValue("/clear")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case msg := <-m.userMsgCh:
		t.Errorf("slash command should drain userMsgCh; got %q", msg)
	default:
	}
	if m.pendingInputAfterTurn != "" {
		t.Errorf("pendingInputAfterTurn should be cleared; got %q", m.pendingInputAfterTurn)
	}
}
