package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNormalizePasteLineBreaks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"cr to lf", "adapter\ragent\rauth", "adapter\nagent\nauth"},
		{"crlf to lf", "adapter\r\nagent\r\nauth", "adapter\nagent\nauth"},
		{"mixed cr and crlf", "a\rb\r\nc\rd", "a\nb\nc\nd"},
		{"trailing cr", "wizard\rworktree\r", "wizard\nworktree\n"},
		{"no line breaks untouched", "hello world", "hello world"},
		{"lf only untouched", "a\nb\nc", "a\nb\nc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(normalizePasteLineBreaks([]rune(tt.in)))
			if got != tt.want {
				t.Errorf("normalizePasteLineBreaks(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Regression: a CR-separated multi-line paste (terminals transmit paste
// line breaks as CR, not LF) used to slip past the '\n' multi-line check,
// land in the textarea with raw CRs, and — once submitted — overprint the
// transcript echo from column 0, mangling the line into garbage like
// "skillsegopslwmponent". Normalized, it must take the large-paste marker
// detour like any LF-separated paste and round-trip with LF line breaks.
func TestPaste_CRSeparatedTakesLargePasteDetour(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.PasteMsg{Content: "adapter\ragent\rauth"})

	got := m.textInput.Value()
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("raw CR reached the textarea: %q", got)
	}
	if !strings.Contains(got, "[Pasted text #1: 3 lines, 18 bytes]") {
		t.Fatalf("CR-separated paste should be stashed behind a marker, got %q", got)
	}
	expanded := m.expandPastes(got)
	if want := "adapter\nagent\nauth"; expanded != want {
		t.Errorf("expanded paste = %q, want %q", expanded, want)
	}
}

func TestPaste_SmallPasteWithoutLineBreaksInsertsVerbatim(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.PasteMsg{Content: "hello world"})

	if got := m.textInput.Value(); got != "hello world" {
		t.Errorf("textarea = %q, want %q", got, "hello world")
	}
}

// Bracketed multi-line pastes often include one terminal newline at the end.
// The marker summary and round trip should describe the content, not count that
// transport terminator as a phantom blank line.
func TestPaste_TrimTrailingTransportNewline(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.PasteMsg{Content: "one\ntwo\n"})

	got := m.textInput.Value()
	if !strings.Contains(got, "[Pasted text #1: 2 lines, 7 bytes]") {
		t.Fatalf("paste marker should ignore trailing transport newline, got %q", got)
	}
	if expanded := m.expandPastes(got); expanded != "one\ntwo" {
		t.Fatalf("expanded paste = %q, want trimmed content", expanded)
	}
}
