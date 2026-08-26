package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCmdlineClick_SingleLineIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("hello world")
	m.textInput.CursorStart()
	startLine := m.textInput.Line()
	startCol := m.textInput.Column()

	ox, oy := m.inputFrameOrigin()
	const targetCol = 6 // 'w' in "hello world"
	x := ox + 2 + inputPromptW + targetCol
	y := oy + 1

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd != nil {
		t.Fatalf("cmdline clicks should not produce commands, got %T", cmd)
	}
	if m.textInput.Line() != startLine {
		t.Errorf("Line() = %d, want unchanged %d", m.textInput.Line(), startLine)
	}
	if m.textInput.Column() != startCol {
		t.Errorf("Column() = %d, want unchanged %d because mouse clicks are ignored", m.textInput.Column(), startCol)
	}
}

func TestCmdlineClick_MultiLineIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("first\nsecond")
	m.textInput.CursorStart()
	startLine := m.textInput.Line()
	startCol := m.textInput.Column()

	ox, oy := m.inputFrameOrigin()
	const targetCol = 3 // 'o' in "second"
	x := ox + 2 + inputPromptW + targetCol
	y := oy + 1 + 1 // second visual row

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd != nil {
		t.Fatalf("cmdline clicks should not produce commands, got %T", cmd)
	}
	if m.textInput.Line() != startLine {
		t.Errorf("Line() = %d, want unchanged %d", m.textInput.Line(), startLine)
	}
	if m.textInput.Column() != startCol {
		t.Errorf("Column() = %d, want unchanged %d because mouse clicks are ignored", m.textInput.Column(), startCol)
	}
}

func TestCmdlineClick_EmptyValueIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true

	ox, oy := m.inputFrameOrigin()
	m, cmd := applyMsg(m, tea.MouseClickMsg{X: ox + 4, Y: oy + 1})
	if cmd != nil {
		t.Fatalf("cmdline clicks should not produce commands, got %T", cmd)
	}
	if m.textInput.Value() != "" {
		t.Errorf("clicking an empty cmdline should not insert or change anything; got %q", m.textInput.Value())
	}
	if m.cmdlineClickFlash {
		t.Fatal("clicking an empty cmdline should not trigger the focus-color pulse")
	}
}

func TestCmdlineClick_HeroEmptyValueIsIgnored(t *testing.T) {
	m := newTestModel(t)
	if m.enteredConversation {
		t.Fatal("test setup: fresh model should still be on the welcome screen")
	}

	ox, oy := m.inputFrameOrigin()
	m, cmd := applyMsg(m, tea.MouseClickMsg{X: ox + 4, Y: oy + 1})
	if cmd != nil {
		t.Fatalf("welcome-screen cmdline clicks should not produce commands, got %T", cmd)
	}
	if m.cmdlineClickFlash {
		t.Fatal("clicking the welcome-screen cmdline should not trigger the focus-color pulse")
	}
	if m.enteredConversation {
		t.Fatal("clicking the welcome-screen cmdline should not start the conversation")
	}
}

func TestCmdlineClick_PastEndOfLineIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.textInput.SetValue("hi")
	m.textInput.CursorStart()
	startLine := m.textInput.Line()
	startCol := m.textInput.Column()

	ox, oy := m.inputFrameOrigin()
	x := ox + 2 + inputPromptW + 50 // far past "hi"'s end
	y := oy + 1

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd != nil {
		t.Fatalf("cmdline clicks should not produce commands, got %T", cmd)
	}
	if m.textInput.Line() != startLine {
		t.Errorf("Line() = %d, want unchanged %d", m.textInput.Line(), startLine)
	}
	if m.textInput.Column() != startCol {
		t.Errorf("Column() = %d, want unchanged %d because mouse clicks are ignored", m.textInput.Column(), startCol)
	}
}
