package tui

import "testing"

// TestRunMemoryReindex_DefersToCommand guards the U2 fix: the blocking
// Ollama probe + per-memory embed loop must run in a tea.Cmd, not inline
// on the Update goroutine. So runMemoryReindex closes the picker
// immediately and hands the work back as a non-nil command. Regression for
// the release audit's memory-reindex-blocks-ui-loop finding.
func TestRunMemoryReindex_DefersToCommand(t *testing.T) {
	m := newTestModel(t)
	m.memoryPickerOpen = true

	m2, cmd := m.runMemoryReindex()

	if m2.memoryPickerOpen {
		t.Error("picker should close immediately, before the (deferred) work runs")
	}
	if cmd == nil {
		t.Error("reindex work must be deferred to a tea.Cmd (nil cmd means it ran inline and froze the UI)")
	}
}
