package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSummarizing_BlocksNewTurn is a regression for the release audit's
// summarize-turn-race-dataloss / summarize-clobbers-concurrent-turn
// findings: a turn submitted while a (multi-minute) summarize runs would
// have its user message + reply silently dropped when summaryDoneMsg
// replaces history with the pre-summarize snapshot. Enter must not start
// a turn during summarize, and the typed text must be preserved.
func TestSummarizing_BlocksNewTurn(t *testing.T) {
	m := newTestModel(t)
	before := len(m.sess.Messages)
	m.summarizing = true
	m.textInput.SetValue("hello during summarize")

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.turnActive {
		t.Error("a turn started during summarize; its messages would be clobbered by summaryDoneMsg")
	}
	if len(m.sess.Messages) != before {
		t.Errorf("history changed during summarize: %d → %d", before, len(m.sess.Messages))
	}
	if got := strings.TrimSpace(m.textInput.Value()); got != "hello during summarize" {
		t.Errorf("typed text not preserved during summarize: %q", got)
	}
}

// TestSummarizing_AllowsSlashCommands confirms the guard is scoped to
// turns only — a slash command (read-only, no history clobber) still runs
// while summarizing, so the box is cleared as usual.
func TestSummarizing_AllowsSlashCommands(t *testing.T) {
	m := newTestModel(t)
	m.summarizing = true
	m.textInput.SetValue("/help")

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.textInput.Value(); got != "" {
		t.Errorf("slash command not dispatched during summarize; box = %q", got)
	}
}
