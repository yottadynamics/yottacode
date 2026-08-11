package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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

func TestModeStylesAreForegroundOnlyAcrossThemes(t *testing.T) {
	defer ApplyTheme(themes.DefaultName) // restore for subsequent tests

	for _, name := range themes.Names() {
		t.Run(name, func(t *testing.T) {
			ApplyTheme(name)
			rendered := []string{
				stylePlanBannerLabel.Render(PlanModeIcon + " plan mode"),
				styleAutoBannerLabel.Render(AutoModeIcon + " auto mode"),
				styleYoloBannerLabel.Render(YoloModeIcon + " yolo mode"),
				renderModeStatus([]string{"plan"}),
				renderModeStatus([]string{"auto"}),
				renderModeStatus([]string{"yolo"}),
			}
			for _, got := range rendered {
				if strings.Contains(got, "\x1b[7m") || strings.Contains(got, "\x1b[27m") {
					t.Fatalf("mode styles must not use reverse video: %q", got)
				}
				if lipgloss.Width(got) == 0 {
					t.Fatalf("mode style rendered no visible label for theme %q", name)
				}
			}
		})
	}
}
