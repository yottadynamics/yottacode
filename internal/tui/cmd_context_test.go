package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestSlash_ContextRendersAllSections fires /context against a stub
// model and checks the expected section headers + the totals line are
// present. Asserts on stripped output so style codes don't break the
// match.
func TestSlash_ContextRendersAllSections(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleSystem, Content: "you are an assistant."},
		adapter.Message{Role: adapter.RoleUser, Content: "hello"},
	)
	m, _ = typeAndEnter(t, m, "/context")
	got := ansi.Strip(m.transcript.String())

	for _, want := range []string{
		"Context Usage",
		"Estimated usage by category",
		"System prompt:",
		"System tools:",
		"MCP tools:",
		"Memory files:",
		"Skills:",
		"Messages:",
		"Free space:",
		"MCP tools",
		"/mcp",
		"Memory files",
		"/memory",
		"Skills",
		"/skills",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/context output missing %q\n---\n%s", want, got)
		}
	}
}

// TestSlash_ContextBucketSumLEWindow asserts the underlying invariant
// that drives the bar: the used buckets never exceed the resolved
// window, so the bar paint loop can't overrun. We don't render here —
// just compute against the helpers — to keep the assertion crisp.
func TestSlash_ContextBucketSumLEWindow(t *testing.T) {
	m := newTestModel(t)
	// Stuff a substantial system prompt + body so several buckets are
	// non-zero.
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: strings.Repeat("s", 400)},
		{Role: adapter.RoleUser, Content: strings.Repeat("u", 1200)},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{Name: "read_file", ArgsJSON: `{"path":"main.go"}`},
		}},
	}
	window := m.contextWindow()
	if window <= 0 {
		t.Skip("test model has no resolvable window")
	}

	sysTok, convoTok := contextwindow_SplitMessages(t, m)
	sysTools, mcpTools := contextToolTokens(&m)
	used := sysTok + sysTools + mcpTools + convoTok
	// Memory and skills are zero in the test fixture.
	if used > window {
		t.Errorf("sum of used buckets (%d) exceeds window (%d)", used, window)
	}
}

// contextwindow_SplitMessages is a tiny test-local re-call to avoid a
// new import alias in the file — `import _` of contextwindow would be
// awkward when we just need the values. We re-derive them inline.
func contextwindow_SplitMessages(t *testing.T, m Model) (sys int, convo int) {
	t.Helper()
	for _, msg := range m.sess.Messages {
		c := len(msg.Content)
		for _, tc := range msg.ToolCalls {
			c += len(tc.Name) + len(tc.ArgsJSON)
		}
		const cpt = 4
		if msg.Role == adapter.RoleSystem {
			sys += (c + cpt - 1) / cpt
		} else {
			convo += (c + cpt - 1) / cpt
		}
	}
	return
}

// TestContextBarWidth_ClampedAcrossTerminalSizes locks down the floor /
// ceiling on the bar width so a narrow split or an ultra-wide terminal
// both render usably.
func TestContextBarWidth_ClampedAcrossTerminalSizes(t *testing.T) {
	cases := []struct {
		name      string
		termWidth int
		wantMin   int
		wantMax   int
	}{
		{"narrow", 20, contextBarMin, contextBarMin},
		{"default-80", 80, contextBarMax - 4, contextBarMax},
		{"wide-200", 200, contextBarMax, contextBarMax},
		{"unset", 0, 60, 70}, // hard-coded fallback in contextBarWidth
	}
	for _, c := range cases {
		got := contextBarWidth(c.termWidth)
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("contextBarWidth(%d) = %d, want in [%d, %d]",
				c.termWidth, got, c.wantMin, c.wantMax)
		}
	}
}

// TestContextSegmentedBar_PaintsUsedBeforeFree checks the bar fills
// used buckets left-to-right and pads with `░` to width. After
// stripping ANSI we expect the head to contain `█` and the tail to be
// pure `░`.
func TestContextSegmentedBar_PaintsUsedBeforeFree(t *testing.T) {
	buckets := []contextBucket{
		{"a", 1000, colorAccent, true},
		{"b", 500, colorSuccess, true},
		{"free", 8500, colorDim, false},
	}
	bar := contextSegmentedBarPlain(buckets, 10_000, 64)
	if !strings.HasPrefix(bar, "█") {
		t.Errorf("bar should lead with used cells, got %q", bar[:5])
	}
	if !strings.HasSuffix(bar, "░░░") {
		t.Errorf("bar should end with free cells (░), got %q", bar[len(bar)-5:])
	}
	// `█` and `░` are 3-byte runes in UTF-8 — count by rune for the
	// display-width clamp assertion.
	cells := len([]rune(bar))
	if cells < contextBarMin || cells > contextBarMax {
		t.Errorf("bar length %d cells out of clamp range [%d, %d]",
			cells, contextBarMin, contextBarMax)
	}
}

// contextSegmentedBarPlain is a test helper that strips lipgloss
// styling for length / glyph assertions. Keeps the production
// renderer free of test hooks.
func contextSegmentedBarPlain(buckets []contextBucket, window, termWidth int) string {
	return ansi.Strip(renderContextSegmentedBar(buckets, window, termWidth))
}

// TestContextPercentLabel_RoundsAndHandlesEdge covers the small
// percent helper used in the bar trailer.
func TestContextPercentLabel_RoundsAndHandlesEdge(t *testing.T) {
	if got := contextPercentLabel(0, 0); got != "—" {
		t.Errorf("zero window should render em-dash, got %q", got)
	}
	if got := contextPercentLabel(50, 100); got != "50%" {
		t.Errorf("50/100 = %q, want 50%%", got)
	}
	if got := contextPercentLabel(33, 100); got != "33%" {
		t.Errorf("33/100 = %q, want 33%%", got)
	}
	// Cap at 100% to match the status bar — heuristic drift past the
	// window shouldn't render as "110%".
	if got := contextPercentLabel(150, 100); got != "100%" {
		t.Errorf("150/100 = %q, want 100%% (capped)", got)
	}
}
