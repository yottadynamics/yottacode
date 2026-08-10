package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestCmdUsage_OpensOverlayNotHistory locks the core behavior: /usage
// renders into the inline overlay above the cmdline (m.usageOpen +
// m.usagePanel) and does NOT append to chat scrollback, so the token
// tallies never enter the conversation the model re-reads.
func TestCmdUsage_OpensOverlayNotHistory(t *testing.T) {
	m := newTestModel(t)
	beforeLines := len(m.historyLines)

	m, _ = cmdUsage(m, nil)

	if !m.usageOpen {
		t.Errorf("cmdUsage should open the usage overlay")
	}
	if m.usagePanel == "" {
		t.Errorf("cmdUsage should populate the usage panel body")
	}
	if len(m.historyLines) != beforeLines {
		t.Errorf("cmdUsage must not append to chat history: %d -> %d lines",
			beforeLines, len(m.historyLines))
	}

	v := m.View().Content
	if !strings.Contains(m.usagePanel, "──") {
		t.Errorf("/usage should render with submenu horizontal rules: %q", m.usagePanel)
	}
	if !strings.Contains(v, "Usage") {
		t.Errorf("View should render the usage overlay: %q", v)
	}
}

// TestCmdUsage_AnyKeyDismisses confirms the overlay behaves like the
// cheatsheet: any keypress closes it and clears the cached body.
func TestCmdUsage_AnyKeyDismisses(t *testing.T) {
	m := newTestModel(t)
	m.usageOpen = true
	m.usagePanel = "stale body"

	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "x"})

	if m.usageOpen {
		t.Errorf("any key should close the usage overlay")
	}
	if m.usagePanel != "" {
		t.Errorf("closing should clear the cached panel; got %q", m.usagePanel)
	}
}

// TestRenderSessionUsage_TokenBreakdown locks the per-model token
// breakdown shape (matching Claude Code's /usage) and guards the hard
// rule that /usage shows NO dollar figure.
func TestRenderSessionUsage_TokenBreakdown(t *testing.T) {
	s := &session.Session{
		ID:    "20260528-120000.000000",
		Model: "claude-sonnet-4-5",
		TotalUsage: adapter.Usage{
			InputTokens:         12_403,
			OutputTokens:        3_182,
			CacheCreationTokens: 1_920,
			CacheReadTokens:     44_210,
		},
		ModelUsage: map[string]adapter.Usage{
			"claude-sonnet-4-5": {
				InputTokens:         12_403,
				OutputTokens:        3_182,
				CacheCreationTokens: 1_920,
				CacheReadTokens:     44_210,
			},
		},
	}
	got := renderSessionUsage(s)

	for _, want := range []string{
		"session",
		"20260528-120000.000000",
		"claude-sonnet-4-5",
		"input",
		"12,403",
		"output",
		"3,182",
		"cache read",
		"44,210",
		"cache write",
		"1,920",
		"total",
		"session total",
		"61,715 tokens", // 12,403 + 3,182 + 1,920 + 44,210
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing substring %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "$") {
		t.Errorf("/usage must not show a dollar figure; got:\n%s", got)
	}
}

// TestRenderSessionUsage_MetricsAndSimpleSingleModelTotal keeps the polished
// /usage layout from repeating a model total when the session has a single
// plain input/output model row; the explicit session total is the useful
// summary. It also locks the lightweight metrics row.
func TestRenderSessionUsage_MetricsAndSimpleSingleModelTotal(t *testing.T) {
	s := &session.Session{
		ID:         "20260710-003122.173234",
		Model:      "gpt-5.5",
		TotalUsage: adapter.Usage{InputTokens: 33_782, OutputTokens: 66},
		ModelUsage: map[string]adapter.Usage{
			"gpt-5.5": {InputTokens: 33_782, OutputTokens: 66},
		},
		Messages: []adapter.Message{
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{Name: "read_file"}, {Name: "grep"}}},
			{Role: adapter.RoleAssistant},
		},
		SubagentTasks: []subagents.TaskRecord{{ID: "sub1"}},
	}

	got := renderSessionUsage(s)
	for _, want := range []string{
		"metrics",
		"2 turns",
		"2 tools",
		"1 subagent",
		"gpt-5.5",
		"33,782",
		"66",
		"session total",
		"33,848 tokens",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing substring %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "────────────────") || strings.Contains(got, "total       33,848") {
		t.Errorf("single plain model should not repeat a model total before session total; got:\n%s", got)
	}
}

// TestRenderSessionUsage_FoldsSubagents: subagent spend is folded into the
// per-model rows AND the session total — each subagent attributed to its own
// model, inherited-model runs to the session's headline model. The rows sum
// to the total (no separate subagents block; per-task detail lives inline).
func TestRenderSessionUsage_FoldsSubagents(t *testing.T) {
	s := &session.Session{
		ID:         "20260709-120000.000000",
		Model:      "claude-sonnet-4-5",
		TotalUsage: adapter.Usage{InputTokens: 10_000, OutputTokens: 2_000},
		ModelUsage: map[string]adapter.Usage{
			"claude-sonnet-4-5": {InputTokens: 10_000, OutputTokens: 2_000},
		},
		SubagentTasks: []subagents.TaskRecord{
			{ID: "s1", AgentType: "Explore", Model: "claude-haiku-4-5", Usage: adapter.Usage{InputTokens: 5_000, OutputTokens: 500}},
			{ID: "s2", AgentType: "Explore", Model: "", Usage: adapter.Usage{InputTokens: 3_000, OutputTokens: 300}}, // inherits sonnet
		},
	}
	got := renderSessionUsage(s)

	for _, want := range []string{
		// sonnet row folds main (10,000/2,000) + inherited subagent (3,000/300)
		"claude-sonnet-4-5",
		"13,000",
		"2,300",
		// haiku row is the routed subagent
		"claude-haiku-4-5",
		"5,000",
		// total: 12,000 (main) + 8,800 (subagents) = 20,800
		"session total",
		"20,800 tokens",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing substring %q in:\n%s", want, got)
		}
	}
	// Folded, not a separate section: metrics may mention the subagent count,
	// but there should not be a standalone subagents spending block.
	if strings.Contains(got, "subagents\n") {
		t.Errorf("subagent spend must be folded in, not shown as a section; got:\n%s", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("/usage must not show a dollar figure; got:\n%s", got)
	}
}

// TestRenderTurnFooter: the end-of-turn footer shows the turn's exact token
// total when usage was reported, and stays duration-only when it wasn't.
func TestRenderTurnFooter(t *testing.T) {
	withUsage := renderTurnFooter(12*time.Second, adapter.Usage{InputTokens: 3_000, OutputTokens: 200, CacheReadTokens: 40_000})
	if !strings.Contains(withUsage, "◦ thought") {
		t.Errorf("footer must keep the duration receipt; got %q", withUsage)
	}
	if !strings.Contains(withUsage, "tokens") {
		t.Errorf("footer must show tokens when usage is reported; got %q", withUsage)
	}

	noUsage := renderTurnFooter(5*time.Second, adapter.Usage{})
	if strings.Contains(noUsage, "tokens") {
		t.Errorf("footer must omit tokens when usage is zero; got %q", noUsage)
	}
}

// TestRenderSessionUsage_NoData covers the "session ran but adapter
// never reported usage" path (Ollama on an older OpenAI shim, e.g.).
// The block should call this out explicitly rather than rendering a
// row of zeros.
func TestRenderSessionUsage_NoData(t *testing.T) {
	s := &session.Session{ID: "20260528-150000.000000", Model: "m"}
	got := renderSessionUsage(s)
	if !strings.Contains(got, "no token data yet") {
		t.Errorf("expected zero-usage message in:\n%s", got)
	}
}

// TestRenderOpenAIAuthAccount_FromMemo writes the quota memo directly
// (and clears the probe cache) and asserts the panel surfaces plan +
// reset window from just the memo path.
func TestRenderOpenAIAuthAccount_FromMemo(t *testing.T) {
	// Drop a memo with a plan + reset 90 minutes out.
	adapter.SetOpenAIAuthRateLimitForTest(&adapter.OpenAIAuthRateLimit{
		Observed: time.Now(),
		PlanType: "pro",
		Primary: adapter.OpenAIAuthWindow{
			Has:      true,
			ResetsAt: time.Now().Add(90 * time.Minute),
		},
	})
	defer adapter.SetOpenAIAuthRateLimitForTest(nil)
	// Clear any cached probe so the renderer falls back to the memo.
	adapter.SetOpenAIAuthAccountForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())

	if !strings.Contains(got, "pro plan") {
		t.Errorf("missing plan label in:\n%s", got)
	}
	if !strings.Contains(got, "resets in") {
		t.Errorf("missing reset hint in:\n%s", got)
	}
}

// TestRenderOpenAIAuthAccount_BothWindows locks the headline feature:
// when the backend reports both quota windows they render as separate,
// individually-labelled rows, so a user blocked on the short window is
// never shown only the weekly number.
func TestRenderOpenAIAuthAccount_BothWindows(t *testing.T) {
	adapter.SetOpenAIAuthRateLimitForTest(&adapter.OpenAIAuthRateLimit{
		Observed: time.Now(),
		PlanType: "prolite",
		Primary: adapter.OpenAIAuthWindow{
			Has: true, HasPercent: true, UsedPercent: 12,
			WindowMinutes: 300,
			ResetsAt:      time.Now().Add(3*time.Hour + 41*time.Minute),
		},
		Secondary: adapter.OpenAIAuthWindow{
			Has: true, HasPercent: true, UsedPercent: 97,
			WindowMinutes: 10080,
			ResetsAt:      time.Now().Add(6*24*time.Hour + 12*time.Hour),
		},
	})
	defer adapter.SetOpenAIAuthRateLimitForTest(nil)
	adapter.SetOpenAIAuthAccountForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())

	for _, want := range []string{
		"prolite plan",
		"5h window",
		"12% used",
		"resets in 3h 41m",
		"weekly",
		"97% used",
		"resets in 6d 12h",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderOpenAIAuthAccount_OmitsLapsedWindow is the regression test
// for the bug that started this: a window whose reset has already passed
// used to render as a permanent "resets in now", because
// humanizeUsageDuration collapses non-positive durations to "now". A
// lapsed window describes a quota that has since refilled, so it must be
// dropped entirely — leaving only the window still in force.
func TestRenderOpenAIAuthAccount_OmitsLapsedWindow(t *testing.T) {
	adapter.SetOpenAIAuthRateLimitForTest(&adapter.OpenAIAuthRateLimit{
		Observed: time.Now().Add(-8 * time.Hour),
		PlanType: "pro",
		Primary: adapter.OpenAIAuthWindow{
			Has: true, HasPercent: true, UsedPercent: 100,
			WindowMinutes: 300,
			ResetsAt:      time.Now().Add(-2 * time.Hour), // already reset
		},
		Secondary: adapter.OpenAIAuthWindow{
			Has: true, HasPercent: true, UsedPercent: 55,
			WindowMinutes: 10080,
			ResetsAt:      time.Now().Add(48 * time.Hour),
		},
	})
	defer adapter.SetOpenAIAuthRateLimitForTest(nil)
	adapter.SetOpenAIAuthAccountForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())

	if strings.Contains(got, "resets in now") {
		t.Errorf("lapsed window rendered as %q; want the row dropped:\n%s", "resets in now", got)
	}
	if strings.Contains(got, "5h window") {
		t.Errorf("lapsed 5h window still shown in:\n%s", got)
	}
	if !strings.Contains(got, "weekly") {
		t.Errorf("in-force weekly window missing in:\n%s", got)
	}
}

// TestRenderOpenAIAuthAccount_PercentWithoutReset covers the partial
// case: a window reporting usage but no reset instant shows the percent
// alone rather than an invented countdown.
func TestRenderOpenAIAuthAccount_PercentWithoutReset(t *testing.T) {
	adapter.SetOpenAIAuthRateLimitForTest(&adapter.OpenAIAuthRateLimit{
		Observed: time.Now(),
		Primary: adapter.OpenAIAuthWindow{
			Has: true, HasPercent: true, UsedPercent: 30,
			WindowMinutes: 300,
		},
	})
	defer adapter.SetOpenAIAuthRateLimitForTest(nil)
	adapter.SetOpenAIAuthAccountForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())

	if !strings.Contains(got, "30% used") {
		t.Errorf("missing percent in:\n%s", got)
	}
	if strings.Contains(got, "resets in") {
		t.Errorf("invented a countdown with no reset instant in:\n%s", got)
	}
}

// TestRenderOpenAIAuthAccount_NoQuotaObserved confirms the empty state
// points at the first turn (capture is now per-response) rather than the
// old "only after a 429".
func TestRenderOpenAIAuthAccount_NoQuotaObserved(t *testing.T) {
	adapter.SetOpenAIAuthRateLimitForTest(nil)
	adapter.SetOpenAIAuthAccountForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())

	if !strings.Contains(got, "quota shown after the first turn") {
		t.Errorf("missing empty-state hint in:\n%s", got)
	}
}

// TestRenderOpenAIAuthAccount_FromProbe seeds the probe cache and
// confirms the renderer prefers it over the 429 memo when both are
// present — and surfaces the email too.
func TestRenderOpenAIAuthAccount_FromProbe(t *testing.T) {
	adapter.SetOpenAIAuthAccountForTest(&adapter.OpenAIAuthAccount{
		Email:    "user@example.com",
		Plan:     "Plus",
		ProbedAt: time.Now(),
	})
	defer adapter.SetOpenAIAuthAccountForTest(nil)
	adapter.SetOpenAIAuthRateLimitForTest(nil)

	got := renderOpenAIAuthAccount(context.Background())
	if !strings.Contains(got, "Plus plan") {
		t.Errorf("missing probe-supplied plan in:\n%s", got)
	}
	if !strings.Contains(got, "user@example.com") {
		t.Errorf("missing email line in:\n%s", got)
	}
}

// TestRenderRateLimits_ShowsLiveHeadroom confirms the live quota block
// renders both buckets with "remaining / limit" and a reset countdown
// from a seeded snapshot.
func TestRenderRateLimits_ShowsLiveHeadroom(t *testing.T) {
	adapter.SetRateLimitForTest(adapter.ProviderAnthropic, &adapter.RateLimitSnapshot{
		Provider:          adapter.ProviderAnthropic,
		Observed:          time.Now(),
		HasTokens:         true,
		TokensLimit:       2_000_000,
		TokensRemaining:   1_824_000,
		TokensReset:       time.Now().Add(41 * time.Second),
		HasRequests:       true,
		RequestsLimit:     4_000,
		RequestsRemaining: 3_998,
		RequestsReset:     time.Now().Add(41 * time.Second),
	})
	defer adapter.SetRateLimitForTest(adapter.ProviderAnthropic, nil)

	got := renderRateLimits(adapter.ProviderAnthropic)
	for _, want := range []string{
		"rate limits",
		"tokens",
		"1,824,000 / 2,000,000 remaining",
		"requests",
		"3,998 / 4,000 remaining",
		"resets in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderRateLimits_SelfHidesWithoutSnapshot confirms the block is
// empty (not a bare header) when no rate-limit data exists for the
// provider — e.g. local/free providers, or before the first turn.
func TestRenderRateLimits_SelfHidesWithoutSnapshot(t *testing.T) {
	adapter.SetRateLimitForTest(adapter.ProviderOllama, nil)
	if got := renderRateLimits(adapter.ProviderOllama); got != "" {
		t.Errorf("expected empty block, got %q", got)
	}
}

// TestFormatInt_ThousandsSeparators locks the comma-grouped output
// /usage uses for every token tally.
func TestFormatInt_ThousandsSeparators(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		1:          "1",
		999:        "999",
		1000:       "1,000",
		1234:       "1,234",
		1234567:    "1,234,567",
		-1234:      "-1,234",
		1000000000: "1,000,000,000",
	}
	for in, want := range cases {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderToolStats_BreakdownAndErrors locks the per-tool-name table
// shape: one row per tool, sorted by output tokens descending, with an
// error count appended only for tools that had one.
func TestRenderToolStats_BreakdownAndErrors(t *testing.T) {
	s := &session.Session{
		ToolStats: map[string]session.ToolStat{
			"read_file": {Count: 40, OutputTokens: 2_000},
			"bash":      {Count: 5, OutputTokens: 50, Errors: 2},
			"grep":      {Count: 3, OutputTokens: 30},
		},
	}
	got := renderToolStats(s)
	for _, want := range []string{
		"tools", "3 distinct",
		"read_file", "40", "2,000",
		"bash", "5", "2 errors",
		"grep", "3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "grep") && strings.Contains(got, "errors") {
		lines := strings.Split(got, "\n")
		for _, line := range lines {
			if strings.Contains(line, "grep") && strings.Contains(line, "errors") {
				t.Errorf("grep had no errors and should not show an errors clause: %q", line)
			}
		}
	}
}

// TestRenderToolStats_FlagsOutlier: a tool whose output is far above the
// table's mean is flagged, so a runaway call doesn't blend into the list.
func TestRenderToolStats_FlagsOutlier(t *testing.T) {
	s := &session.Session{
		ToolStats: map[string]session.ToolStat{
			"read_file": {Count: 1, OutputTokens: 100_000},
			"bash":      {Count: 1, OutputTokens: 100},
			"grep":      {Count: 1, OutputTokens: 100},
		},
	}
	got := renderToolStats(s)
	if !strings.Contains(got, "outlier") {
		t.Errorf("expected the runaway tool flagged as an outlier in:\n%s", got)
	}
}

// TestRenderToolStats_Empty confirms the section self-hides when the
// session has no recorded tool calls, matching the other optional
// /usage sections' self-hiding convention.
func TestRenderToolStats_Empty(t *testing.T) {
	if got := renderToolStats(&session.Session{}); got != "" {
		t.Errorf("expected empty string with no tool stats; got %q", got)
	}
}

// TestRenderToolStats_TieOrderIsDeterministic is the regression test for a
// real flakiness bug: s.ToolStats is a map (Go randomizes range order) and
// the sort had no tiebreaker, so two tools tied on OutputTokens could
// render in a different relative order each time /usage opened. Runs the
// render many times over the same tied input and requires identical output
// every time.
func TestRenderToolStats_TieOrderIsDeterministic(t *testing.T) {
	s := &session.Session{
		ToolStats: map[string]session.ToolStat{
			"grep": {Count: 3, OutputTokens: 100},
			"glob": {Count: 2, OutputTokens: 100}, // tied with grep
			"read": {Count: 1, OutputTokens: 50},
		},
	}
	first := renderToolStats(s)
	for i := 0; i < 20; i++ {
		if got := renderToolStats(s); got != first {
			t.Fatalf("renderToolStats is nondeterministic on a tie:\nrun 0:\n%s\nrun %d:\n%s", first, i+1, got)
		}
	}
	// The tiebreaker is alphabetical on tool name: "glob" before "grep".
	if strings.Index(first, "glob") > strings.Index(first, "grep") {
		t.Errorf("expected glob before grep (alphabetical tiebreak) in:\n%s", first)
	}
}

// TestRenderSubagentDetail_ListsTasksAndFlagsOutlier locks the per-task
// list shape and its outlier flagging, independent of the folded
// per-model rollup renderSessionUsage already shows.
func TestRenderSubagentDetail_ListsTasksAndFlagsOutlier(t *testing.T) {
	s := &session.Session{
		SubagentTasks: []subagents.TaskRecord{
			{
				ID: "abc12345", AgentType: "Explore", Status: subagents.TaskCompleted, ToolCalls: 5,
				Started: time.Now().Add(-2 * time.Minute), Finished: time.Now(),
				Usage: adapter.Usage{InputTokens: 100_000, OutputTokens: 5_000}, // outlier vs. the two below
			},
			{
				ID: "def67890", AgentType: "Plan", Status: subagents.TaskCompleted, ToolCalls: 2,
				Started: time.Now().Add(-1 * time.Minute), Finished: time.Now(),
				Usage: adapter.Usage{InputTokens: 1_000, OutputTokens: 200},
			},
			{
				ID: "ghi13579", AgentType: "Plan", Status: subagents.TaskCompleted, ToolCalls: 1,
				Started: time.Now().Add(-1 * time.Minute), Finished: time.Now(),
				Usage: adapter.Usage{InputTokens: 800, OutputTokens: 100},
			},
		},
	}
	got := renderSubagentDetail(s)
	for _, want := range []string{"subagents", "3 tasks", "Explore", "Plan", "done", "outlier"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderSubagentDetail_Empty confirms the section self-hides for a
// session with no subagent activity.
func TestRenderSubagentDetail_Empty(t *testing.T) {
	if got := renderSubagentDetail(&session.Session{}); got != "" {
		t.Errorf("expected empty string with no subagent tasks; got %q", got)
	}
}

// TestRenderSubagentDetail_ShowsCompactionCount: a subagent whose own
// in-loop compaction fired shows "compacted Nx" on its row; a task that
// never compacted shows no such clause.
func TestRenderSubagentDetail_ShowsCompactionCount(t *testing.T) {
	s := &session.Session{
		SubagentTasks: []subagents.TaskRecord{
			{
				ID: "abc12345", AgentType: "Explore", Status: subagents.TaskCompleted,
				Started: time.Now().Add(-time.Minute), Finished: time.Now(),
				Usage: adapter.Usage{InputTokens: 1_000}, CompactionCount: 2,
			},
			{
				ID: "def67890", AgentType: "Plan", Status: subagents.TaskCompleted,
				Started: time.Now().Add(-time.Minute), Finished: time.Now(),
				Usage: adapter.Usage{InputTokens: 800},
			},
		},
	}
	got := renderSubagentDetail(s)
	if !strings.Contains(got, "compacted 2x") {
		t.Errorf("expected the compacted task's row to show 'compacted 2x' in:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "def67890") && strings.Contains(line, "compacted") {
			t.Errorf("task with zero CompactionCount should not show a compacted clause: %q", line)
		}
	}
}

// TestCacheHitRate covers the derived cache-hit fraction: -1 (no row)
// when there's no cache read activity, else CacheRead/(CacheRead+Input).
func TestCacheHitRate(t *testing.T) {
	if got := cacheHitRate(adapter.Usage{InputTokens: 1_000}); got != -1 {
		t.Errorf("cacheHitRate with no cache reads = %v, want -1", got)
	}
	if got, want := cacheHitRate(adapter.Usage{InputTokens: 1_000, CacheReadTokens: 9_000}), 0.9; got != want {
		t.Errorf("cacheHitRate = %v, want %v", got, want)
	}
}

// TestFormatModelUsageBlock_CacheHitRateRow confirms the cache-hit row
// appears only when the model actually has cache-read activity.
func TestFormatModelUsageBlock_CacheHitRateRow(t *testing.T) {
	withCache := formatModelUsageBlock("claude-sonnet-4-5", adapter.Usage{InputTokens: 1_000, CacheReadTokens: 9_000}, 20, true)
	if !strings.Contains(withCache, "cache hit") || !strings.Contains(withCache, "90%") {
		t.Errorf("expected a 90%% cache hit row in:\n%s", withCache)
	}
	noCache := formatModelUsageBlock("claude-sonnet-4-5", adapter.Usage{InputTokens: 1_000, OutputTokens: 200}, 20, true)
	if strings.Contains(noCache, "cache hit") {
		t.Errorf("cache hit row should be omitted without cache activity in:\n%s", noCache)
	}
}

// TestFormatModelUsageBlock_CacheHitRateWidthNeverOverflows is the
// regression test for a real column-alignment bug: with small token counts
// but a high hit rate, "100%" (4 chars) is wider than every token count in
// the block, and the shared column width must grow to fit it rather than
// truncating/misaligning the row (and the divider line, which reuses the
// same width).
func TestFormatModelUsageBlock_CacheHitRateWidthNeverOverflows(t *testing.T) {
	// InputTokens: 0 so the hit rate is exactly 100% — "100%" (4 chars) is
	// wider than every token count here (all 3 digits or fewer), which is
	// exactly the case that overflowed the shared column before the fix.
	got := formatModelUsageBlock("m", adapter.Usage{OutputTokens: 20, CacheReadTokens: 80}, 5, true)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	// runeLen, not len: the divider row is built from the multi-byte "─"
	// box-drawing rune, so a raw byte-length comparison would report a
	// false mismatch even when every row renders the same terminal width.
	width := -1
	for _, line := range lines {
		if width == -1 {
			width = runeLen(line)
			continue
		}
		if runeLen(line) != width {
			t.Errorf("row width inconsistent: %q is %d runes, want %d, in:\n%s", line, runeLen(line), width, got)
		}
	}
	if !strings.Contains(got, "100%") {
		t.Errorf("expected a 100%% cache hit row in:\n%s", got)
	}
}

// TestRenderPeakTurn_PicksLargestTurn locks the "largest turn" callout:
// it must pick the assistant turn with the highest reported usage, not
// the last or the first.
func TestRenderPeakTurn_PicksLargestTurn(t *testing.T) {
	s := &session.Session{
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "hi"},
			{Role: adapter.RoleAssistant, Usage: &adapter.Usage{InputTokens: 100, OutputTokens: 50}},
			{Role: adapter.RoleUser, Content: "more"},
			{Role: adapter.RoleAssistant, Usage: &adapter.Usage{InputTokens: 8_000, OutputTokens: 200}}, // largest
			{Role: adapter.RoleUser, Content: "one more"},
			{Role: adapter.RoleAssistant, Usage: &adapter.Usage{InputTokens: 300, OutputTokens: 50}},
		},
	}
	got := renderPeakTurn(s)
	if !strings.Contains(got, "turn 2") {
		t.Errorf("expected turn 2 (the largest) flagged; got %q", got)
	}
	if !strings.Contains(got, "8,200") {
		t.Errorf("expected the largest turn's token total; got %q", got)
	}
}

// TestRenderPeakTurn_NoUsageData confirms an empty result when no
// assistant turn reported usage, rather than a misleading "turn 0".
func TestRenderPeakTurn_NoUsageData(t *testing.T) {
	s := &session.Session{Messages: []adapter.Message{{Role: adapter.RoleAssistant}}}
	if got := renderPeakTurn(s); got != "" {
		t.Errorf("expected empty string with no turn usage; got %q", got)
	}
}

// TestRenderCompactionSummary locks the "compacted Nx / reclaimed ~X
// tokens" line, summed across every recorded event.
func TestRenderCompactionSummary(t *testing.T) {
	s := &session.Session{
		CompactionEvents: []session.CompactionRecord{
			{Before: 100_000, After: 40_000, Auto: true},
			{Before: 120_000, After: 50_000, Auto: false},
		},
	}
	got := renderCompactionSummary(s)
	for _, want := range []string{"compacted 2x", "reclaimed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestRenderCompactionSummary_Empty confirms the line self-hides when
// the session was never compacted.
func TestRenderCompactionSummary_Empty(t *testing.T) {
	if got := renderCompactionSummary(&session.Session{}); got != "" {
		t.Errorf("expected empty string with no compaction events; got %q", got)
	}
}

// TestRenderTodayRollup_PerSessionRowsSumToAggregate locks the expanded
// daily rollup: it must still show the aggregate line, plus one row per
// session with the current session marked.
func TestRenderTodayRollup_PerSessionRowsSumToAggregate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s1, err := session.New("m", "/proj")
	if err != nil {
		t.Fatalf("New s1: %v", err)
	}
	s1.AddUsage("m", &adapter.Usage{InputTokens: 1_000_000})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	s2, err := session.New("m", "/proj")
	if err != nil {
		t.Fatalf("New s2: %v", err)
	}
	s2.AddUsage("m", &adapter.Usage{InputTokens: 200_000})
	if err := s2.Save(); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	got := renderTodayRollup(s2.ID)
	for _, want := range []string{"today", "2 sessions", "(current)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderTodayRollup_SkipsWhenOnlyCurrentSession confirms the rollup
// self-hides when the current session is the only one today (the
// per-session block above already covers it).
func TestRenderTodayRollup_SkipsWhenOnlyCurrentSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := session.New("m", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.AddUsage("m", &adapter.Usage{InputTokens: 100})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if got := renderTodayRollup(s.ID); got != "" {
		t.Errorf("expected empty rollup with only one session today; got %q", got)
	}
}

// TestLocalMidnight_UsesCallersLocation is the regression test for a real
// timezone bug: time.Now().Truncate(24*time.Hour) rounds against the
// UTC-referenced zero time, so a non-UTC caller would silently get the
// most recent UTC midnight instead of their own — e.g. a UTC-5 user's 8pm
// local session falls after UTC midnight and would be wrongly excluded
// from "today". localMidnight must respect the input's own Location.
func TestLocalMidnight_UsesCallersLocation(t *testing.T) {
	utcMinus5 := time.FixedZone("UTC-5", -5*60*60)
	// 8pm local on the 10th is 1am UTC on the 11th — a naive UTC-truncate
	// would compute "today" as the 11th, excluding this moment's own day.
	in := time.Date(2026, 3, 10, 20, 0, 0, 0, utcMinus5)

	got := localMidnight(in)

	if got.Year() != 2026 || got.Month() != 3 || got.Day() != 10 {
		t.Errorf("localMidnight(%v) = %v, want midnight on the 10th (caller's own day)", in, got)
	}
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Errorf("localMidnight(%v) = %v, want exactly 00:00:00", in, got)
	}
	if got.Location() != utcMinus5 {
		t.Errorf("localMidnight must preserve the input's Location; got %v", got.Location())
	}
}

// TestWindowedUsagePanel_NoScrollWhenFits confirms short content passes
// through unchanged — no scroll hint appended when everything already
// fits on screen.
func TestWindowedUsagePanel_NoScrollWhenFits(t *testing.T) {
	m := newTestModel(t)
	m.height = 40
	m.usagePanel = "line1\nline2\nline3"
	if got := m.windowedUsagePanel(); got != m.usagePanel {
		t.Errorf("expected panel unchanged when it fits; got %q", got)
	}
}

// TestWindowedUsagePanel_WindowsAndHints confirms content taller than the
// terminal is sliced to the visible window and a scroll-position hint is
// appended — the popup has no height clamp of its own (popup.go), so this
// is what keeps a long panel from silently overflowing.
func TestWindowedUsagePanel_WindowsAndHints(t *testing.T) {
	m := newTestModel(t)
	m.height = 10 // visible = 10 - 2 (border) - 1 (reserve) = 7
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i)
	}
	m.usagePanel = strings.Join(lines, "\n")
	m.usageScrollOffset = 0

	got := m.windowedUsagePanel()
	if !strings.Contains(got, "line0") {
		t.Errorf("expected the first page to start at line0 in:\n%s", got)
	}
	if strings.Contains(got, "line7") {
		t.Errorf("expected only 7 lines visible, but found line7 in:\n%s", got)
	}
	if !strings.Contains(got, "of 20 lines") {
		t.Errorf("expected a scroll-position hint in:\n%s", got)
	}
}

// TestUsageVisibleLines_NeverOverflowsAboveHardFloor is the regression test
// for a real bug: the scroll-position hint line windowedUsagePanel appends
// wasn't counted against usageVisibleLines' budget, so at small (but not
// absurdly small) terminal heights the rendered box — content + hint + the
// 2 popupBox border rows — exceeded the terminal. Checks every height down
// to the true hard floor (below ~4 rows no bordered popup can render
// anything, a limit shared by every popup in this package).
func TestUsageVisibleLines_NeverOverflowsAboveHardFloor(t *testing.T) {
	m := newTestModel(t)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "x"
	}
	m.usagePanel = strings.Join(lines, "\n")

	for _, height := range []int{6, 8, 10, 24, 40} {
		m.height = height
		m.usageScrollOffset = 0
		got := m.windowedUsagePanel()
		rendered := len(strings.Split(got, "\n")) + 2 // + popupBox's 2 border rows
		if rendered > height {
			t.Errorf("height=%d: rendered box needs %d rows (content+hint+border), overflowing the terminal", height, rendered)
		}
	}
}

// TestUsageMaxScrollOffset locks the clamp so PgDn can't scroll past the
// point where the last line is at the bottom of the visible window.
func TestUsageMaxScrollOffset(t *testing.T) {
	m := newTestModel(t)
	m.height = 10 // visible = 7
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "x"
	}
	m.usagePanel = strings.Join(lines, "\n")
	if got, want := m.usageMaxScrollOffset(), 13; got != want { // 20 lines - 7 visible
		t.Errorf("usageMaxScrollOffset() = %d, want %d", got, want)
	}
}
