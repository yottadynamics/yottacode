package tui

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// TestApplyTheme_MonochromeOnlyOnNoColor locks down that themeMonochrome
// (buildStyles' mirror of Palette.Monochrome, read by
// newMarkdownRenderer) is true for "no-color" and false for every
// other registered theme — a future palette added without opting into
// Monochrome should stay colored by default, and no-color must always
// carry it.
func TestApplyTheme_MonochromeOnlyOnNoColor(t *testing.T) {
	defer ApplyTheme(themes.DefaultName) // restore for subsequent tests

	for _, name := range themes.Names() {
		ApplyTheme(name)
		want := name == "no-color"
		if themeMonochrome != want {
			t.Errorf("ApplyTheme(%q): themeMonochrome = %v, want %v", name, themeMonochrome, want)
		}
	}
}
