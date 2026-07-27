package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestBuildLabel_NoCommit(t *testing.T) {
	got := buildLabel("0.1.0", "", false)
	if got != "v0.1.0" {
		t.Fatalf("buildLabel(no commit) = %q, want %q", got, "v0.1.0")
	}
}

func TestBuildLabel_CleanCommit(t *testing.T) {
	got := buildLabel("0.1.0", "e546e47", false)
	if got != "v0.1.0 · e546e47" {
		t.Fatalf("buildLabel(clean) = %q, want %q", got, "v0.1.0 · e546e47")
	}
}

func TestBuildLabel_DirtyCommit(t *testing.T) {
	got := buildLabel("0.1.0", "e546e47", true)
	if got != "v0.1.0 · e546e47*" {
		t.Fatalf("buildLabel(dirty) = %q, want %q", got, "v0.1.0 · e546e47*")
	}
}

// Dirty without a commit shouldn't render an orphan asterisk — without
// VCS info we can't tell what's dirty, so fall back to plain.
func TestBuildLabel_DirtyWithoutCommit(t *testing.T) {
	got := buildLabel("0.1.0", "", true)
	if got != "v0.1.0" {
		t.Fatalf("buildLabel(dirty no commit) = %q, want %q", got, "v0.1.0")
	}
}

// labelRow renders "label  value" with no colon — alignment alone
// communicates the relationship per the Phase 1 palette discipline.
// Catches regressions where someone re-adds `:` or strips the padding.
func TestLabelRow_NoColonRightPaddedLabel(t *testing.T) {
	got := labelRow("model", 9, "gpt-4o")
	plain := stripANSI(got)
	if strings.Contains(plain, ":") {
		t.Errorf("label row should not contain a colon: %q", plain)
	}
	// "model" (5) + 4 spaces of padding to width 9 + 2 separator = 11
	// chars before the value starts.
	if !strings.HasPrefix(plain, "model      gpt-4o") {
		t.Errorf("label should be right-padded to width and separated by two spaces: %q", plain)
	}
}

// The startup card does not surface inline `/model to change` or
// `/provider for details` suffixes — Phase 1 dropped them. Catches
// regressions where someone re-adds them.
func TestRenderStartupBox_DropsInlineCommandHints(t *testing.T) {
	got := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", 0)
	plain := stripANSI(got)
	if strings.Contains(plain, "/model to change") {
		t.Errorf("startup card should not carry the /model hint: %q", plain)
	}
	if strings.Contains(plain, "/provider for details") {
		t.Errorf("startup card should not carry the /provider hint: %q", plain)
	}
}

// On a narrow terminal the box must stay within termWidth — the long
// rotating tip used to extend the auto-sized box past the terminal
// width, and the bordered output then wrapped in scrollback as
// stair-step "─────┐" ghosts on every resize. Guards against a
// regression where the tip (or any other row) is rendered without
// being wrapped to the available inner width.
func TestRenderStartupBox_WrapsToTerminalWidth(t *testing.T) {
	longTip := "`/sessions` opens the picker (Load / Resume / Rename / Export); Load shows recent sessions, Resume takes an id or name directly."
	const termWidth = 80
	got := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, longTip, termWidth)
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > termWidth {
			t.Errorf("line wider than termWidth=%d (got %d): %q", termWidth, w, stripANSI(line))
		}
	}
}

// On fresh sessions the tip renders inside the bordered card.
func TestRenderStartupBox_TipInsideCard(t *testing.T) {
	withTip := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "drop preferences into ~/.yottacode/USER.md", 80)
	if !strings.Contains(stripANSI(withTip), "tip:") {
		t.Errorf("startup output should include `tip:` prefix when a tip is provided: %q", stripANSI(withTip))
	}
	if !strings.Contains(stripANSI(withTip), "USER.md") {
		t.Errorf("startup output should render the tip body: %q", stripANSI(withTip))
	}
	lines := strings.Split(stripANSI(withTip), "\n")
	if strings.HasPrefix(lines[len(lines)-1], "tip:") {
		t.Errorf("tip should not render below the card, got last line %q in %q", lines[len(lines)-1], stripANSI(withTip))
	}
	foundInside := false
	for _, line := range lines {
		if strings.Contains(line, "tip:") && strings.HasPrefix(line, "│ ") {
			foundInside = true
			break
		}
	}
	if !foundInside {
		t.Errorf("tip should render as a bordered row inside the card: %q", stripANSI(withTip))
	}
	noTip := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", 0)
	if strings.Contains(stripANSI(noTip), "tip:") {
		t.Errorf("empty tip should suppress the footer: %q", stripANSI(noTip))
	}
}

func TestRenderStartupBox_HeaderUsesBrandAndBuildEdges(t *testing.T) {
	got := stripANSI(renderStartupBox("0.3.0", "ff9dbeb", true, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", 80))
	if !strings.Contains(got, ">_ YottaCode v0.3.0 (ff9dbeb*)") {
		t.Fatalf("startup card missing left-aligned product/build mark: %q", got)
	}
	if strings.Contains(got, "by YottaDynamics") {
		t.Fatalf("startup card should not include company byline: %q", got)
	}
	first := strings.Split(got, "\n")[0]
	if !strings.HasPrefix(first, "┌─ >_ YottaCode v0.3.0 (ff9dbeb*) ") {
		t.Fatalf("startup header should embed brand and build label on the left border, got %q", first)
	}
}

// TestStartupBox_ShowsSessionID: the id is the handle for `sessions resume`,
// and the exit hint only prints it on the way out (and only once the session
// has a turn). Showing it up front makes it available mid-session.
func TestStartupBox_ShowsSessionID(t *testing.T) {
	got := stripANSI(renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo",
		"20260721-024553.488321", "main", "USER", adapter.ProviderProfile{
			Provider: adapter.ProviderOpenAI,
		}, "", 0))
	if !strings.Contains(got, "session") || !strings.Contains(got, "20260721-024553.488321") {
		t.Errorf("startup card should carry the session id:\n%s", got)
	}
}

// TestStartupBox_OmitsEmptySessionID keeps the card clean when no id is
// available (early frames, fixtures) rather than printing a blank row.
func TestStartupBox_OmitsEmptySessionID(t *testing.T) {
	got := stripANSI(renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo",
		"", "main", "USER", adapter.ProviderProfile{
			Provider: adapter.ProviderOpenAI,
		}, "", 0))
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "session") {
			t.Errorf("empty id should render no session row, got %q", line)
		}
	}
}

func TestStartupBox_ShowsExperimentalFlagsInsideCard(t *testing.T) {
	got := stripANSI(renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo",
		"20260721-024553.488321", "main", "USER", adapter.ProviderProfile{
			Provider: adapter.ProviderOpenAI,
		}, "", 100, "lsp_code_intelligence"))
	if !strings.Contains(got, "experimental") || !strings.Contains(got, "lsp_code_intelligence") {
		t.Fatalf("startup card should carry enabled experimental flags:\n%s", got)
	}
	if !strings.Contains(got, "/experimental for details") {
		t.Fatalf("startup card should keep the experimental details hint:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "lsp_code_intelligence") && !strings.HasPrefix(line, "│ ") {
			t.Fatalf("experimental row should render inside the startup card, got %q in:\n%s", line, got)
		}
	}
}
