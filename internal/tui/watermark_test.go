package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// stripANSI is shared with diff_test.go — see that file for the
// implementation note. We use it here to assert plain text in the
// rendered context-bar segment without coupling to ANSI escape codes.

// renderContextBar emits `ctx <used> / <max> (<pct>%)` — the compact
// status form keeps the useful numbers and percentage without the old
// six-cell graph.

// TestDeriveCompactionThreshold pins the replacement for the old
// user-facing compaction_threshold knob: the in-loop trigger fraction is
// derived from auto_threshold instead, always staying strictly ahead of it
// (compactionThresholdBelowAuto) so mid-turn compaction preempts the
// turn-boundary auto-summarize it exists to avoid, with a fixed default
// and an absolute floor so a pathological auto_threshold can't derive
// something absurd.
func TestDeriveCompactionThreshold(t *testing.T) {
	cases := []struct {
		name          string
		autoThreshold float64
		want          float64
	}{
		{"default auto_threshold uses the default fraction", 0.85, 0.70},
		{"auto disabled (1.0) disables this too", 1.0, 1.0},
		{"auto threshold just above default+margin keeps default", 0.81, 0.70},
		{"tight auto_threshold pulls the fraction down with it", 0.50, 0.40},
		{"low auto_threshold still clamps to the floor, staying below it", 0.15, 0.10},
		{"pathologically low auto_threshold: floor exceeds auto (documented tradeoff)", 0.05, 0.10},
	}
	for _, c := range cases {
		if got := deriveCompactionThreshold(c.autoThreshold); got != c.want {
			t.Errorf("%s: deriveCompactionThreshold(%.2f) = %.2f, want %.2f", c.name, c.autoThreshold, got, c.want)
		}
	}
}

// TestDeriveCompactionThreshold_AlwaysBelowAutoWhenFeasible checks the
// invariant deriveCompactionThreshold exists to guarantee — in-loop
// compaction fires before the turn-boundary auto-summarize — holds across
// the whole reachable auto_threshold range, except the documented
// pathological-low-value tradeoff (see deriveCompactionThreshold's comment).
func TestDeriveCompactionThreshold_AlwaysBelowAutoWhenFeasible(t *testing.T) {
	for auto := 0.20; auto < 1.0; auto += 0.01 {
		got := deriveCompactionThreshold(auto)
		if got >= auto {
			t.Errorf("deriveCompactionThreshold(%.2f) = %.2f, want strictly below auto_threshold", auto, got)
		}
	}
}

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

// TestAutoSuppressedByNonConvergence guards the fix for the gate that used
// to latch shut for the whole session: after a summarize failed to
// converge, auto-summarize was disabled until /clear. The suppressor must
// hold back a pointless re-run at the same fill+window, but release when
// fill grows or the window changes.
func TestAutoSuppressedByNonConvergence(t *testing.T) {
	const window = 32_000
	cases := []struct {
		name                string
		pct                 float64
		window              int
		nonConvergentAt     float64
		nonConvergentWindow int
		want                bool
	}{
		{"no record — never suppresses", 0.95, window, 0, 0, false},
		{"same fill+window — suppresses", 0.91, window, 0.91, window, true},
		{"tiny growth within step — still suppresses", 0.93, window, 0.91, window, true},
		{"grew a full step past — releases (retry)", 0.97, window, 0.91, window, false},
		{"window changed (bigger) — releases", 0.91, 200_000, 0.91, window, false},
		{"window changed (drift-shrunk) — releases", 0.95, 24_000, 0.91, window, false},
	}
	for _, c := range cases {
		got := autoSuppressedByNonConvergence(c.pct, c.window, c.nonConvergentAt, c.nonConvergentWindow)
		if got != c.want {
			t.Errorf("%s: autoSuppressedByNonConvergence(%.2f, %d, %.2f, %d) = %v, want %v",
				c.name, c.pct, c.window, c.nonConvergentAt, c.nonConvergentWindow, got, c.want)
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
	for _, glyph := range []string{"█", "░", "▓"} {
		if strings.Contains(plain, glyph) {
			t.Errorf("context segment should not render graph glyph %q: %q", glyph, plain)
		}
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
