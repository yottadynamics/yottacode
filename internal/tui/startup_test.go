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

func TestRenderStartupBox_EmbedsTitleAndVersionInTopBorder(t *testing.T) {
	got := renderStartupBox("0.3.0", "148eb2b", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", 80)
	plain := stripANSI(got)
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		t.Fatalf("startup card should have a border and body rows:\n%s", plain)
	}

	if !strings.Contains(lines[0], ">_ YottaCode") {
		t.Fatalf("top border should embed product label:\n%s", plain)
	}
	if !strings.Contains(lines[0], "v0.3.0 (148eb2b)") {
		t.Fatalf("top border should embed version label:\n%s", plain)
	}
	if strings.Contains(lines[0], "YottaDynamics") {
		t.Fatalf("startup header should not include company text:\n%s", plain)
	}
	versionIdx := strings.Index(lines[0], "v0.3.0 (148eb2b)")
	titleIdx := strings.Index(lines[0], ">_ YottaCode")
	if versionIdx < 0 || titleIdx < 0 {
		t.Fatalf("top border should include both version and title:\n%s", plain)
	}
	if titleIdx > versionIdx {
		t.Fatalf("title should render before version in the top border:\n%s", plain)
	}
	if strings.Contains(lines[0], "YottaCode  v0.3.0") {
		t.Fatalf("version/title spacing should be tight in the top border:\n%s", plain)
	}
	if !strings.Contains(lines[0], ">_ YottaCode v0.3.0 (148eb2b)") {
		t.Fatalf("top border should render title followed by version:\n%s", plain)
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, ">_ YottaCode") || strings.Contains(line, "v0.3.0") {
			t.Fatalf("title/version should stay in top border only, got body line %q:\n%s", line, plain)
		}
	}
}

func TestRenderStartupBox_SpansCmdlineWidth(t *testing.T) {
	const termWidth = 100
	got := stripANSI(renderStartupBox("0.3.0", "148eb2b", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", termWidth))

	lines := strings.Split(got, "\n")
	for _, line := range lines {
		if w := lipgloss.Width(line); w != termWidth {
			t.Fatalf("startup card row width = %d, want %d for %q:\n%s", w, termWidth, line, got)
		}
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

// On fresh sessions the tip is folded into the card as a dim footer
// line. Empty tip omits the footer.
func TestRenderStartupBox_TipFooter(t *testing.T) {
	withTip := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "drop preferences into ~/.yottacode/USER.md", 0)
	if !strings.Contains(stripANSI(withTip), "tip:") {
		t.Errorf("card should embed `tip:` prefix when a tip is provided: %q", stripANSI(withTip))
	}
	if !strings.Contains(stripANSI(withTip), "USER.md") {
		t.Errorf("card should render the tip body: %q", stripANSI(withTip))
	}
	noTip := renderStartupBox("0.1.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider: adapter.ProviderOpenAI,
	}, "", 0)
	if strings.Contains(stripANSI(noTip), "tip:") {
		t.Errorf("empty tip should suppress the footer: %q", stripANSI(noTip))
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
