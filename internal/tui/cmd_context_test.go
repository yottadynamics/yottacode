package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// TestSlash_ContextRendersAllSections fires /context against a stub
// model and checks the expected section headers + the totals line are
// present in the overlay body. Asserts on stripped output so style
// codes don't break the match.
func TestSlash_ContextRendersAllSections(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleSystem, Content: "you are an assistant."},
		adapter.Message{Role: adapter.RoleUser, Content: "hello"},
	)
	m, _ = typeAndEnter(t, m, "/context")

	if !m.contextReportOpen {
		t.Fatal("/context should open the inline report overlay")
	}
	got := ansi.Strip(m.contextReportBody)

	for _, want := range []string{
		"Context Usage",
		"Estimated usage by category",
		"System prompt:",
		"System tools:",
		"MCP tools:",
		"Memory files:",
		"Messages:",
		"Free space:",
		"MCP tools",
		"/mcp",
		"Memory files",
		"/memory",
		"Skills",
		"/skills",
		"press any key to exit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/context output missing %q\n---\n%s", want, got)
		}
	}

	// The report is transient inspection — it must NOT leak into chat
	// history / the transcript (which feeds /export and resume replay).
	if tr := ansi.Strip(m.transcript.String()); strings.Contains(tr, "Context Usage") {
		t.Errorf("/context output leaked into the transcript:\n%s", tr)
	}

	// Any key dismisses the overlay (mirrors the cheatsheet).
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.contextReportOpen {
		t.Error("esc should dismiss the /context overlay")
	}
	if m.contextReportBody != "" {
		t.Error("dismissing the overlay should release the cached body")
	}
}

// TestContext_SkillBodiesDoNotCountAgainstWindow is the regression for
// the /context Skills-bucket bug: skill bodies load on demand and are
// not in-window, so attaching a large body to a skill must not change
// the reported window usage. The Skills section still lists the skill
// as on-disk inventory, tagged "(on demand)".
func TestContext_SkillBodiesDoNotCountAgainstWindow(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "you are an assistant."},
		{Role: adapter.RoleUser, Content: "hi"},
	}

	base := newTestModel(t)
	base.sess.Messages = msgs
	base.skills = nil

	withSkill := newTestModel(t)
	withSkill.sess.Messages = msgs
	withSkill.skills = []skills.Skill{{
		Name:        "huge-skill",
		Description: "x",
		Body:        strings.Repeat("B", 100_000), // ~25K tokens of body
		Source:      skills.ScopeBuiltin,
	}}

	baseReport := ansi.Strip(renderContextReport(&base))
	skillReport := ansi.Strip(renderContextReport(&withSkill))

	// The 100KB body must not move the window total: the used / window
	// summary line is identical with and without the skill.
	if a, b := lineWith(baseReport, " / "), lineWith(skillReport, " / "); a != b {
		t.Errorf("skill body changed the reported window usage:\n without: %q\n with:    %q", a, b)
	}

	// Skills is no longer a counted legend bucket. "Skills:" (with colon)
	// only ever came from the legend row; the section header is "Skills"
	// without a colon, so its absence is a clean assertion.
	if strings.Contains(skillReport, "Skills:") {
		t.Errorf("Skills must not appear as a counted legend bucket:\n%s", skillReport)
	}

	// The inventory still lists the skill, tagged on demand.
	if !strings.Contains(skillReport, "huge-skill") {
		t.Errorf("Skills inventory should still list the skill:\n%s", skillReport)
	}
	if !strings.Contains(skillReport, "on demand") {
		t.Errorf("Skills inventory rows should be tagged (on demand):\n%s", skillReport)
	}
}

// lineWith returns the first line of s containing sub, or "" if none.
func lineWith(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
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
	// Memory is zero in the test fixture; skills are inventory-only and
	// deliberately not part of `used` (see renderContextReport).
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
