package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// stripANSI is shared with diff_test.go — see that file for the
// implementation note. We use it here to assert plain text in the
// rendered context-bar segment without coupling to ANSI escape codes.

// renderContextBar emits `ctx ████░░ <used> / <max> (<pct>%)` —
// label in Dim, then a six-cell █/░ fill bar, the abbreviated
// used/max token counts, and the percentage in parentheses. Bar +
// value are colored by tier (Dim under warn_threshold, Warning amber
// once crossed, Error red once auto_threshold is crossed). The
// legacy ▓ glyph rendered inconsistently across fonts; █ + ░ is the
// chosen pair.

func TestSummaryConverged(t *testing.T) {
	cases := []struct {
		name      string
		tokens    int
		window    int
		threshold float64
		want      bool
	}{
		{"under threshold converges", 40_000, 64_000, 0.85, true},
		{"at threshold does not converge", 54_400, 64_000, 0.85, false},
		{"over threshold does not converge", 81_000, 64_000, 0.85, false},
		{"zero window converges (nothing to loop on)", 81_000, 0, 0.85, true},
		{"disabled threshold (1.0) converges", 999_999, 64_000, 1.0, true},
		{"disabled threshold (0) converges", 999_999, 64_000, 0, true},
	}
	for _, c := range cases {
		if got := summaryConverged(c.tokens, c.window, c.threshold); got != c.want {
			t.Errorf("%s: summaryConverged(%d, %d, %.2f) = %v, want %v",
				c.name, c.tokens, c.window, c.threshold, got, c.want)
		}
	}
}

func TestRenderContextBar_BelowThreshold(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		WarnThreshold: 0.65,
		AutoThreshold: 0.85,
	}}
	m.contextTokens = 100 // 10% — well below warn

	got := m.renderContextBar()
	plain := stripANSI(got)
	if !strings.HasPrefix(plain, "ctx ") {
		t.Errorf("context segment should start with `ctx ` label: %q", plain)
	}
	if !strings.Contains(plain, "10%") {
		t.Errorf("context segment should report 10%%: %q", plain)
	}
	// 10% of a 6-cell bar rounds to 1 filled cell (█) + 5 empty cells (░).
	if !strings.Contains(plain, "█") {
		t.Errorf("context segment should include at least one filled cell at 10%%: %q", plain)
	}
	if !strings.Contains(plain, "░") {
		t.Errorf("context segment should include empty cells at 10%%: %q", plain)
	}
	if strings.Contains(plain, "▓") {
		t.Errorf("context segment should not render the legacy ▓ glyph: %q", plain)
	}
}

func TestRenderContextBar_WarnTier(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		WarnThreshold: 0.65,
		AutoThreshold: 0.85,
	}}
	m.contextTokens = 700 // 70% — above warn, below auto

	got := m.renderContextBar()
	plain := stripANSI(got)
	if !strings.Contains(plain, "70%") {
		t.Errorf("context segment should report 70%%: %q", plain)
	}
}

func TestRenderContextBar_ErrorTier(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		WarnThreshold: 0.65,
		AutoThreshold: 0.85,
	}}
	m.contextTokens = 950 // 95% — above auto

	got := m.renderContextBar()
	plain := stripANSI(got)
	if !strings.Contains(plain, "95%") {
		t.Errorf("context segment should report 95%%: %q", plain)
	}
}

// When the model's context window is unknown (window=0) the segment
// renders empty rather than falling back to a lifetime-tokens counter.
// renderStatus skips empty parts so the user sees just the model
// name + cwd until usage info becomes meaningful.
func TestRenderContextBar_UnknownWindowReturnsEmpty(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{DefaultWindow: 0}}
	m.modelName = "weird-unrecognized-model"
	if got := m.renderContextBar(); got != "" {
		t.Errorf("unknown window should render empty, got %q", got)
	}
}

// The bar shows the abbreviated used/total token count followed by
// the percentage in parentheses — `<used> / <max> (<pct>%)`. Below
// 1K the raw integer renders; from 1K up the K/M abbreviation kicks
// in. The legacy `ctx=N/N` form stays excluded — that one had no
// units and no percentage; the redesigned form keeps both.
func TestRenderContextBar_ShowsTokenCount(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		WarnThreshold: 0.65,
		AutoThreshold: 0.85,
	}}
	m.contextTokens = 100
	plain := stripANSI(m.renderContextBar())
	if !strings.Contains(plain, "100 / 1.0K (10%)") {
		t.Errorf("status bar should report `<used> / <max> (<pct>%%)`: %q", plain)
	}
	if strings.Contains(plain, "ctx=") {
		t.Errorf("status bar should not render the legacy ctx=N/N form: %q", plain)
	}
}

// Token counts at or above 1K render with K/M abbreviation so the
// status bar doesn't widen disproportionately on large context
// windows (200K would be six digits raw).
func TestRenderContextBar_AbbreviatesLargeTokenCounts(t *testing.T) {
	cases := []struct {
		tokens int
		want   string
	}{
		{tokens: 999, want: "999"},
		{tokens: 1500, want: "1.5K"},
		{tokens: 12345, want: "12K"},
		{tokens: 200000, want: "200K"},
	}
	for _, tc := range cases {
		m := newTestModel(t)
		m.fileCfg = config.Config{Context: config.ContextConfig{
			DefaultWindow: 1000000,
			WarnThreshold: 0.65,
			AutoThreshold: 0.85,
		}}
		m.contextTokens = tc.tokens
		plain := stripANSI(m.renderContextBar())
		if !strings.Contains(plain, tc.want) {
			t.Errorf("tokens=%d: expected %q in bar, got %q", tc.tokens, tc.want, plain)
		}
	}
}
