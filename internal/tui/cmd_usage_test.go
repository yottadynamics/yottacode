package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestCmdUsage_OpensOverlayNotHistory locks the core behavior: /usage
// renders into the inline overlay below the cmdline (m.usageOpen +
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

	v := m.View()
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

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

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
	if !strings.Contains(withUsage, "Thought for") {
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

// TestRenderOpenAIAuthAccount_FromMemo simulates an observed 429 by
// writing the rate-limit memo directly (and clearing the probe
// cache) and asserts the panel surfaces plan + reset window from
// just the memo path.
func TestRenderOpenAIAuthAccount_FromMemo(t *testing.T) {
	// Drop a memo with a plan + reset 90 minutes out.
	adapter.SetOpenAIAuthRateLimitForTest(&adapter.OpenAIAuthRateLimit{
		Observed: time.Now(),
		PlanType: "pro",
		ResetsAt: time.Now().Add(90 * time.Minute),
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
