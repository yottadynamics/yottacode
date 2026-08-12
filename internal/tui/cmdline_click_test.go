package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCmdlineClick_SingleLineMovesTextCursor(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("hello world")
	m.textInput.CursorStart()

	ox, oy := m.inputFrameOrigin()
	const targetCol = 6 // 'w' in "hello world"
	x := ox + 2 + inputPromptW + targetCol
	y := oy + 1

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.textInput.Line() != 0 {
		t.Errorf("Line() = %d, want 0", m.textInput.Line())
	}
	if m.textInput.Column() != targetCol {
		t.Errorf("Column() = %d, want %d", m.textInput.Column(), targetCol)
	}
}

func TestCmdlineClick_MultiLineMovesTextCursor(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("first\nsecond")
	m.textInput.CursorStart()

	ox, oy := m.inputFrameOrigin()
	const targetCol = 3 // 'o' in "second"
	x := ox + 2 + inputPromptW + targetCol
	y := oy + 1 + 1 // second visual row

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.textInput.Line() != 1 {
		t.Errorf("Line() = %d, want 1", m.textInput.Line())
	}
	if m.textInput.Column() != targetCol {
		t.Errorf("Column() = %d, want %d", m.textInput.Column(), targetCol)
	}
}

func TestCmdlineClick_EmptyValueFlashesFocusWithoutTyping(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true

	ox, oy := m.inputFrameOrigin()
	m, _ = applyMsg(m, tea.MouseClickMsg{X: ox + 4, Y: oy + 1})
	if m.textInput.Value() != "" {
		t.Errorf("clicking an empty cmdline should not insert or change anything; got %q", m.textInput.Value())
	}
	if !m.cmdlineClickFlash {
		t.Fatal("clicking an empty cmdline should trigger the focus-color pulse")
	}

	m, _ = applyMsg(m, cmdlineClickFlashDoneMsg{})
	if m.cmdlineClickFlash {
		t.Fatal("focus-color pulse should clear after its timer message")
	}
}

func TestCmdlineClick_HeroEmptyValueFlashesFocus(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the welcome screen")
	}

	ox, oy := m.inputFrameOrigin()
	m, _ = applyMsg(m, tea.MouseClickMsg{X: ox + 4, Y: oy + 1})
	if !m.cmdlineClickFlash {
		t.Fatal("clicking the welcome-screen cmdline should trigger the focus-color pulse")
	}
	if m.enteredConversation {
		t.Fatal("clicking the welcome-screen cmdline should not start the conversation")
	}
}

func TestCmdlineClick_PastEndOfLineClampsToEnd(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("hi")
	m.textInput.CursorStart()

	ox, oy := m.inputFrameOrigin()
	x := ox + 2 + inputPromptW + 50 // far past "hi"'s end
	y := oy + 1

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.textInput.Line() != 0 {
		t.Errorf("Line() = %d, want 0", m.textInput.Line())
	}
	if m.textInput.Column() != 2 {
		t.Errorf("Column() = %d, want 2 (clamped to end of \"hi\")", m.textInput.Column())
	}
}
