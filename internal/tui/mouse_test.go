package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestView_DisablesMouseBeforeConversationStarts(t *testing.T) {
	m := newTestModel(t)
	if got := m.View().MouseMode; got != 0 {
		t.Errorf("View().MouseMode = %v, want disabled before transcript exists", got)
	}
}

func TestView_EnablesWheelOnlyMouseForConversation(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeCellMotion for wheel scrolling", got)
	}
}

func TestView_DoesNotEnableAllMotionForInteractiveSurfaces(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.cheatsheetOpen = true
	if got := m.View().MouseMode; got == tea.MouseModeAllMotion {
		t.Errorf("View().MouseMode = %v, want no all-motion mouse handling", got)
	}
}

// newSelectableTranscriptModel builds a model with real, multi-line transcript
// content and a known viewport size, ready for mouse-wheel scroll tests.
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
		t.Error("wheel-up should scroll the transcript viewport away from the bottom")
	}

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelDown})
	if !m.transcriptViewport.AtBottom() {
		t.Error("wheel-down should return the transcript viewport to the bottom")
	}
}

func TestMouseWheel_IgnoresScreenRegionAndScrollsTranscript(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.inputHistory = []string{"first command", "second command"}
	_, y := m.inputFrameOrigin()

	m, _ = applyMsg(m, tea.MouseWheelMsg{X: 4, Y: y + 1, Button: tea.MouseWheelUp})
	if got := m.textInput.Value(); got != "" {
		t.Fatalf("wheel over cmdline should not browse input history, got %q", got)
	}
	if m.transcriptViewport.AtBottom() {
		t.Error("wheel over cmdline should still scroll the transcript")
	}
}

func TestMouseWheel_ScrollsTranscriptWhilePopupOpen(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.cheatsheetOpen = true

	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.transcriptViewport.AtBottom() {
		t.Error("wheel scroll should still move transcript while a popup is open")
	}
	if !m.cheatsheetOpen {
		t.Error("wheel scrolling should not close popups")
	}
}

func TestMouseWheel_NoopBeforeFirstMessage(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the launch hero")
	}
	m, _ = applyMsg(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.transcriptSelecting {
		t.Error("wheel scroll on the hero should not start a selection")
	}
}

func TestMouseClick_IsNoop(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m, cmd := applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if cmd != nil {
		t.Fatalf("clicks should not produce commands, got %T", cmd)
	}
	if m.transcriptSelecting {
		t.Fatal("clicking the transcript should not start yottacode-owned selection")
	}
}

func TestMouseClick_DoesNotActivateWelcomeAction(t *testing.T) {
	m := newTestModel(t)
	m, cmd := applyMsg(m, tea.MouseClickMsg{X: 6, Y: 10})
	if cmd != nil {
		t.Fatalf("welcome clicks should not produce commands, got %T", cmd)
	}
	if m.helpOpen {
		t.Fatal("clicking the welcome Help row should not open help")
	}
}

func TestMouseMotion_ClearsStaleSelectionAndDoesNotHover(t *testing.T) {
	m := newTestModel(t)
	m.welcomeCursor = int(welcomeNewWorktree)
	m.transcriptSelecting = true

	m, cmd := applyMsg(m, tea.MouseMotionMsg{X: 6, Y: 10})
	if cmd != nil {
		t.Fatalf("mouse motion should not produce commands, got %T", cmd)
	}
	if m.transcriptSelecting {
		t.Fatal("mouse motion should clear stale yottacode-owned selection state")
	}
	if got := welcomeAction(m.welcomeCursor); got != welcomeNewWorktree {
		t.Fatalf("hover should not move welcome cursor, got %v", got)
	}
}

func TestMouseRelease_IsNoop(t *testing.T) {
	m := newSelectableTranscriptModel(t)
	m.transcriptSelecting = true
	m, cmd := applyMsg(m, tea.MouseReleaseMsg{X: 12, Y: 0})
	if cmd != nil {
		t.Fatalf("mouse release should not copy to clipboard, got %T", cmd)
	}
	if m.transcriptSelecting {
		t.Fatal("mouse release should clear stale selection state")
	}
}
