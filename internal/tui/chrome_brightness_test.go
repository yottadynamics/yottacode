package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// TestChromeReadsBright guards the brightening of the three persistent
// chrome surfaces — the startup card ("main box"), the cmdline box, and
// the status/task bar. Two rules:
//
//   - TEXT reads in Content (bright), never Dim or Rule.
//   - FRAMES/DIVIDERS (box borders, status-bar middle dots) read in Dim
//     — brighter than the old Rule, but a step below the Content text.
//
// Only state signals (the connection dot, ctx warn/error tiers) stay
// saturated. Escapes are derived from the live palette so the check
// survives a theme reshuffle.
func TestChromeReadsBright(t *testing.T) {
	// Pin a known palette and force truecolor so style escapes are
	// actually emitted (lipgloss renders plain ASCII under `go test`).
	prevTheme := ActiveTheme()
	prevProfile := lipgloss.ColorProfile()
	ApplyTheme(themes.DefaultName)
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prevProfile)
		ApplyTheme(prevTheme)
	})

	// Opening SGR escape for each role, taken from the live colors.
	esc := func(c lipgloss.TerminalColor) string {
		open, _, _ := strings.Cut(lipgloss.NewStyle().Foreground(c).Render("x"), "x")
		return open
	}
	contentEsc, dimEsc, ruleEsc := esc(colorContent), esc(colorDim), esc(colorRule)
	if contentEsc == "" || dimEsc == "" || ruleEsc == "" ||
		contentEsc == dimEsc || dimEsc == ruleEsc || contentEsc == ruleEsc {
		t.Fatalf("need distinct Content/Dim/Rule escapes, got %q / %q / %q", contentEsc, dimEsc, ruleEsc)
	}

	// --- built styles: cmdline box + splash label use Content --------
	for name, style := range map[string]lipgloss.Style{
		"styleSplashLabel":      styleSplashLabel,
		"styleInputPlaceholder": styleInputPlaceholder,
		"styleInputHint":        styleInputHint,
	} {
		if got := style.GetForeground(); got != lipgloss.TerminalColor(colorContent) {
			t.Errorf("%s: foreground = %v, want Content (%v)", name, got, colorContent)
		}
	}
	// The two chevrons (live prompt + scrollback user echo) render in
	// the brand green — maintainer call: no blue on the prompt; green
	// matches the rest of the brand chrome while keeping the anchor
	// role. Equally bright as Content, never Dim/Rule; they must stay
	// identical so the echo mirrors the live bar.
	for name, style := range map[string]lipgloss.Style{
		"styleInputPrompt": styleInputPrompt,
		"styleUserBar":     styleUserBar,
	} {
		if got := style.GetForeground(); got != lipgloss.TerminalColor(colorBrand) {
			t.Errorf("%s: foreground = %v, want brand green (%v)", name, got, colorBrand)
		}
	}

	// --- text reads bright (Content), never Dim/Rule -----------------
	bright := func(label, s string) {
		t.Helper()
		if !strings.Contains(s, contentEsc) {
			t.Errorf("%s: expected Content text: %q", label, s)
		}
		if strings.Contains(s, dimEsc) || strings.Contains(s, ruleEsc) {
			t.Errorf("%s: text should be Content, not Dim/Rule: %q", label, s)
		}
	}
	bright("startup label", labelRow("model", 9, "all-minilm:latest"))
	bright("provider summary", renderProviderSummary(adapter.ProviderProfile{Provider: adapter.ProviderOpenAICompatible}))
	bright("provider tag", renderProviderTag("ollama"))

	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000, WarnThreshold: 0.65, AutoThreshold: 0.85,
	}}
	m.contextTokens = 100 // 10% — below warn, so the bar is in its default tier
	bright("context bar", m.renderContextBar())

	// --- frames/dividers brightened off Rule onto Dim ----------------
	offRule := func(label, s string) {
		t.Helper()
		if strings.Contains(s, ruleEsc) {
			t.Errorf("%s: still renders Rule chrome; borders/dividers should be Dim: %q", label, s)
		}
		if !strings.Contains(s, dimEsc) {
			t.Errorf("%s: expected a Dim border/divider: %q", label, s)
		}
	}
	card := renderStartupBox("0.2.0", "abc1234", true, "all-minilm:latest", "/repo",
		"main", "USER", adapter.ProviderProfile{Provider: adapter.ProviderOpenAICompatible}, "", 120)
	offRule("startup card border", card)
	offRule("cmdline box frame", m.renderInputFrame())

	m.connection = connOK
	m.providerLabel = "ollama" // ensure the provider divider renders
	offRule("status bar dividers", m.renderStatus())
}
