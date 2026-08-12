package tui

import (
	"image/color"
	"io"
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

func TestOSCSetBackground_FormatsCorrectSequence(t *testing.T) {
	got := oscSetBackground(color.RGBA{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff})
	want := "\x1b]11;rgb:1e/1e/2e\x07"
	if got != want {
		t.Errorf("oscSetBackground = %q, want %q", got, want)
	}
}

// captureStdout temporarily redirects os.Stdout to a pipe for the
// duration of fn, returning whatever fn wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

func TestRestoreTerminalBackground_NoopWhenNil(t *testing.T) {
	out := captureStdout(t, func() { restoreTerminalBackground(nil) })
	if out != "" {
		t.Errorf("restoreTerminalBackground(nil) should write nothing (unsupported terminal — never captured an original), got %q", out)
	}
}

func TestRestoreTerminalBackground_WritesOSCSequence(t *testing.T) {
	orig := color.RGBA{R: 0x28, G: 0x28, B: 0x28, A: 0xff}
	out := captureStdout(t, func() { restoreTerminalBackground(orig) })
	want := oscSetBackground(orig)
	if out != want {
		t.Errorf("restoreTerminalBackground wrote %q, want %q", out, want)
	}
}

func TestModel_View_BackgroundColorReflectsTheme(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	// Simulate a terminal that answered the startup OSC 11 query.
	m.originalTerminalBackground = color.RGBA{R: 1, G: 2, B: 3, A: 0xff}

	ApplyTheme("gruvbox") // HasBackground: true
	if got := m.View().BackgroundColor; got != themeBackground {
		t.Errorf("themed palette should set View.BackgroundColor to the theme's background, got %#v want %#v", got, themeBackground)
	}

	ApplyTheme("terminal") // HasBackground: false — adapts to the real terminal
	if got := m.View().BackgroundColor; got != m.originalTerminalBackground {
		t.Errorf("non-backgrounded theme should fall back to the captured original, got %#v want %#v", got, m.originalTerminalBackground)
	}
}

func TestModel_View_NoBackgroundColorWithoutCapturedOriginal(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m.originalTerminalBackground = nil // terminal never answered the startup query

	ApplyTheme("gruvbox")
	if got := m.View().BackgroundColor; got != nil {
		t.Errorf("no captured original background should mean no real-background repaint attempt at all, got %#v", got)
	}
}

// TestThemesPicker_EscRevertsRealBackgroundToo empirically validates the
// "comes for free" design: the OSC-11 hook lives inside buildStyles
// (styles.go), the same chokepoint applyHighlightedTheme's live preview
// already funnels every cursor move through, so the picker's existing
// Esc handler (ApplyTheme(originalTheme)) reverts the real terminal
// background too with zero picker-specific code.
func TestThemesPicker_EscRevertsRealBackgroundToo(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	ApplyTheme("terminal") // start on a non-backgrounded theme
	if hasThemeBackground {
		t.Fatalf("test setup: terminal theme should not have a real background")
	}

	m := newTestModel(t)
	ApplyTheme("terminal") // start on a non-backgrounded theme after New applies defaults
	m, _ = m.runSlash("/theme")
	for i := 0; i < len(m.themePicker.entries) && m.themePicker.entries[m.themePicker.cursor] != "gruvbox"; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.themePicker.entries[m.themePicker.cursor] != "gruvbox" {
		t.Fatalf("test setup: could not navigate picker cursor to gruvbox")
	}
	if !hasThemeBackground {
		t.Fatalf("navigating to gruvbox should live-apply its real background")
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if hasThemeBackground {
		t.Errorf("Esc should revert the real background along with everything else, but hasThemeBackground is still true")
	}
}
