package tui

import (
	"fmt"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestView_LeavesMouseNativeBeforeConversationStarts(t *testing.T) {
	m := newTestModel(t)
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("View().MouseMode = %v, want MouseModeNone", got)
	}
}

func TestView_EnablesMouseForScrollableConversation(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeCellMotion", got)
	}
}

func TestView_EnablesMouseForPopups(t *testing.T) {
	m := newTestModel(t)
	m.cheatsheetOpen = true
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeCellMotion", got)
	}
}

// newSelectableTranscriptModel builds a model with real, multi-line
// transcript content and a known viewport size, ready for mouse
// selection/scroll tests.
func newSelectableTranscriptModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.enteredConversation = true
	for i := range 50 {
		m.appendLine(fmt.Sprintf("history line %d", i))
	}
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestMouseWheel_ScrollsTranscript(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelUp})
	if m.transcriptViewport.AtBottom() {
		t.Error("scrolling the wheel up over the transcript should move the transcript viewport away from the bottom")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	if !m.transcriptViewport.AtBottom() {
		t.Error("scrolling the wheel back down over the transcript should return the transcript viewport to the bottom")
	}
}

func TestMouseWheel_OverCmdlineBrowsesInputHistory(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.inputHistory = []string{"first command", "second command"}
	if !m.transcriptViewport.AtBottom() {
		t.Fatalf("test setup: expected the viewport to start at the bottom")
	}

	_, y := m.inputFrameOrigin()
	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 4, Y: y + 1, Button: tea.MouseWheelUp})
	if got := m.textInput.Value(); got != "second command" {
		t.Fatalf("wheel-up over cmdline should browse input history, got %q", got)
	}
	if !m.transcriptViewport.AtBottom() {
		t.Error("wheel-up over cmdline should not scroll the transcript")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 4, Y: y + 1, Button: tea.MouseWheelDown})
	if got := m.textInput.Value(); got != "" {
		t.Errorf("wheel-down over cmdline should return to the empty draft, got %q", got)
	}
}

func TestMouseWheel_NoopWhilePopupOpen(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.cheatsheetOpen = true

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if !m.transcriptViewport.AtBottom() {
		t.Error("wheel scroll should be a no-op while a popup is open")
	}
}

func TestMouseWheel_NoopBeforeFirstMessage(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the launch hero")
	}
	// Should not panic or otherwise misbehave against a transcriptViewport
	// with no content — the hero has nothing to scroll.
	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.transcriptSelecting {
		t.Error("wheel scroll on the hero should not start a selection")
	}
}

// clipboardText extracts the string payload from a tea.SetClipboard Cmd
// via reflection — the underlying setClipboardMsg type is unexported,
// but its Kind is String, so reflect.Value.String() returns the real
// content directly.
func clipboardText(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd, got nil")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.String {
		t.Fatalf("expected a string-kind clipboard message, got %T", msg)
	}
	return v.String()
}

func TestTranscriptSelection_ClickDragReleaseCopiesToClipboard(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	// AtBottom, so the last visible row is "history line 49".
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("mouse-down over the transcript should begin a selection")
	}
	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 12, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("motion mid-drag should keep the selection active")
	}

	var cmd tea.Cmd
	m, cmd = applyMsg(m, tea.MouseReleaseMsg{X: 12, Y: 0})
	if m.transcriptSelecting {
		t.Error("release should end the drag")
	}
	got := clipboardText(t, cmd)
	if got == "" {
		t.Error("release should copy non-empty selected text to the clipboard")
	}
}

func TestTranscriptSelection_ClickWithNoDragCopiesNothing(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 5, Y: 0})
	m, cmd := applyMsg(m, tea.MouseReleaseMsg{X: 5, Y: 0})
	if cmd != nil {
		t.Errorf("a click with no drag should not produce a clipboard write, got a non-nil Cmd")
	}
	if m.transcriptSelecting {
		t.Error("release should always end the drag, even a no-op one")
	}
}

func TestTranscriptSelection_ClearedWhenPopupOpensMidDrag(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.transcriptSelecting {
		t.Fatalf("test setup: expected an active selection")
	}
	m.cheatsheetOpen = true

	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 10, Y: 0})
	if m.transcriptSelecting {
		t.Error("a popup opening mid-drag should clear the selection, not keep extending it")
	}
}

func TestTranscriptSelection_NoSelectionOnHero(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the launch hero")
	}
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if m.transcriptSelecting {
		t.Error("clicking on the launch hero should not begin a transcript selection")
	}
}

func TestTranscriptSelection_NoopWhilePopupOpen(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.cheatsheetOpen = true
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if m.transcriptSelecting {
		t.Error("clicking while a popup is open should not begin a transcript selection")
	}
}

func TestMouseClick_DismissesStaticPopups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  func(*Model)
		check func(Model) bool
	}{
		{"cheatsheet", func(m *Model) { m.cheatsheetOpen = true }, func(m Model) bool { return m.cheatsheetOpen }},
		{"usage", func(m *Model) { m.usageOpen = true; m.usagePanel = "x" }, func(m Model) bool { return m.usageOpen }},
		{"experimental", func(m *Model) { m.experimentalOpen = true }, func(m Model) bool { return m.experimentalOpen }},
		{"help", func(m *Model) { m.helpOpen = true }, func(m Model) bool { return m.helpOpen }},
		{"contextReport", func(m *Model) { m.contextReportOpen = true }, func(m Model) bool { return m.contextReportOpen }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			tc.open(&m)
			m, _ = applyMsg(m, tea.MouseClickMsg{X: 3, Y: 3})
			if tc.check(m) {
				t.Errorf("clicking anywhere should dismiss the %s popup", tc.name)
			}
		})
	}
}
