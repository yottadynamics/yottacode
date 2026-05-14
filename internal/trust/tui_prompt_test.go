package trust

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTrustPickerModel_StartsOnYes(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	if m.index != 0 {
		t.Errorf("starting index = %d, want 0 (Yes)", m.index)
	}
}

func TestTrustPickerModel_DownThenEnterReturnsNo(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(trustPickerModel)
	if m.index != 1 {
		t.Fatalf("after down: index = %d, want 1", m.index)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(trustPickerModel)
	if !m.answered {
		t.Error("Enter should set answered=true")
	}
	if m.result != PromptNo {
		t.Errorf("result = %v, want PromptNo", m.result)
	}
	if cmd == nil {
		t.Error("Enter should return tea.Quit cmd")
	}
}

func TestTrustPickerModel_EnterReturnsYesByDefault(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(trustPickerModel)
	if m.result != PromptYes {
		t.Errorf("result = %v, want PromptYes", m.result)
	}
	if cmd == nil {
		t.Error("Enter should return tea.Quit cmd")
	}
}

func TestTrustPickerModel_EscReturnsNo(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(trustPickerModel)
	if m.result != PromptNo {
		t.Errorf("result = %v, want PromptNo", m.result)
	}
	if cmd == nil {
		t.Error("Esc should return tea.Quit cmd")
	}
}

func TestTrustPickerModel_UpClampsAtTop(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(trustPickerModel)
	if m.index != 0 {
		t.Errorf("up from top: index = %d, want 0", m.index)
	}
}

func TestTrustPickerModel_DownClampsAtBottom(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo", index: 1}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(trustPickerModel)
	if m.index != 1 {
		t.Errorf("down from bottom: index = %d, want 1", m.index)
	}
}

func TestTrustPickerModel_ViewIncludesCwdAndOptions(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo"}
	view := m.View()
	for _, want := range []string{"Accessing workspace", "/home/me/repo", "1. Yes, I trust this folder", "2. No, exit"} {
		if !containsLiteral(view, want) {
			t.Errorf("view missing %q\nview was:\n%s", want, view)
		}
	}
}

func TestTrustPickerModel_ViewHiddenAfterAnswer(t *testing.T) {
	m := trustPickerModel{cwd: "/home/me/repo", answered: true, result: PromptYes}
	if v := m.View(); v != "" {
		t.Errorf("answered view should be empty, got %q", v)
	}
}

func containsLiteral(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
