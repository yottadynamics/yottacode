package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBuildLabel_NoCommit(t *testing.T) {
	got := buildLabel("0.1.0", "", false)
	if got != "v0.1.0" {
		t.Fatalf("buildLabel(no commit) = %q, want %q", got, "v0.1.0")
	}
}

func TestBuildLabel_CleanCommit(t *testing.T) {
	got := buildLabel("0.1.0", "e546e47", false)
	if got != "v0.1.0 (e546e47)" {
		t.Fatalf("buildLabel(clean) = %q, want %q", got, "v0.1.0 (e546e47)")
	}
}

func TestBuildLabel_DirtyCommit(t *testing.T) {
	got := buildLabel("0.1.0", "e546e47", true)
	if got != "v0.1.0 (e546e47*)" {
		t.Fatalf("buildLabel(dirty) = %q, want %q", got, "v0.1.0 (e546e47*)")
	}
}

func TestBuildLabel_DirtyWithoutCommit(t *testing.T) {
	got := buildLabel("0.1.0", "", true)
	if got != "v0.1.0" {
		t.Fatalf("buildLabel(dirty no commit) = %q, want %q", got, "v0.1.0")
	}
}

func TestLabelRow_NoColonRightPaddedLabel(t *testing.T) {
	got := labelRow("model", 9, "gpt-4o")
	plain := stripANSI(got)
	if strings.Contains(plain, ":") {
		t.Errorf("label row should not contain a colon: %q", plain)
	}
	if !strings.HasPrefix(plain, "model      gpt-4o") {
		t.Errorf("label should be right-padded to width and separated by two spaces: %q", plain)
	}
}

func TestRenderStartupBox_WelcomeActions(t *testing.T) {
	got := stripANSI(renderStartupBox("0.3.0", "148eb2b", false, "", 0, 80))
	for _, want := range []string{
		">_ yottacode v0.3.0 (148eb2b)",
		"Welcome. What are we building today?",
		"› New worktree",
		"Resume session",
		"Enter plan mode",
		"Help",
		"ctrl+r",
		"ctrl+p",
		"ctrl+w",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("startup card missing %q:\n%s", want, got)
		}
	}
	for _, removed := range []string{"provider", "directory", "context", "mem=", "branch="} {
		if strings.Contains(got, removed) {
			t.Fatalf("startup card should not duplicate status data %q:\n%s", removed, got)
		}
	}
}

func TestRenderStartupBox_SelectedRowMovesChevron(t *testing.T) {
	got := stripANSI(renderStartupBox("0.3.0", "", false, "", 2, 80))
	if !strings.Contains(got, "› Enter plan mode") {
		t.Fatalf("selected row should carry chevron:\n%s", got)
	}
	if strings.Contains(got, "› New worktree") {
		t.Fatalf("only selected row should carry chevron:\n%s", got)
	}
}

func TestRenderStartupBox_SpansCmdlineWidth(t *testing.T) {
	const termWidth = 100
	got := stripANSI(renderStartupBox("0.3.0", "148eb2b", false, "", 0, termWidth))
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w != termWidth {
			t.Fatalf("startup card row width = %d, want %d for %q:\n%s", w, termWidth, line, got)
		}
	}
}

func TestRenderStartupBox_WrapsToTerminalWidth(t *testing.T) {
	longTip := "`/sessions` opens the picker (Load / Resume / Rename / Export); Load shows recent sessions, Resume takes an id or name directly."
	const termWidth = 80
	got := renderStartupBox("0.1.0", "abc1234", false, longTip, 0, termWidth)
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > termWidth {
			t.Errorf("line wider than termWidth=%d (got %d): %q", termWidth, w, stripANSI(line))
		}
	}
}

func TestRenderStartupBox_TipFooter(t *testing.T) {
	withTip := renderStartupBox("0.1.0", "abc1234", false, "drop preferences into ~/.yottacode/USER.md", 0, 80)
	if !strings.Contains(stripANSI(withTip), "Tip") {
		t.Errorf("card should embed Tip prefix: %q", stripANSI(withTip))
	}
	if !strings.Contains(stripANSI(withTip), "USER.md") {
		t.Errorf("card should render the tip body: %q", stripANSI(withTip))
	}
	noTip := renderStartupBox("0.1.0", "abc1234", false, "", 0, 80)
	if strings.Contains(stripANSI(noTip), "Tip") {
		t.Errorf("empty tip should suppress the footer: %q", stripANSI(noTip))
	}
}
