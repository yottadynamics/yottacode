package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
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
	if !strings.Contains(v, "usage") {
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

// TestRenderSessionUsage_Anthropic exercises the per-session block
// on a fully-priced model. Locks the per-model breakdown shape
// (matching Claude Code's /usage) and asserts the cost line
// includes a "~$" prefix + the catalog version footer.
func TestRenderSessionUsage_Anthropic(t *testing.T) {
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
	profile := adapter.ProviderProfile{
		Provider:               adapter.ProviderAnthropic,
		SupportsUsageReporting: true,
	}
	got := renderSessionUsage(s, profile)

	for _, want := range []string{
		"session  20260528-120000.000000",
		"usage by model:",
		"claude-sonnet-4-5:",
		"12,403 input",
		"3,182 output",
		"44,210 cache read",
		"1,920 cache write",
		"total cost  ~$",
		"prices catalog",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing substring %q in:\n%s", want, got)
		}
	}
}

// TestRenderSessionUsage_SubscriptionProvider asserts the openai-auth
// path renders the "subscription" label instead of a dollar figure.
// Tokens still show — users still want to see how much they consumed
// regardless of whether the call is flat-fee.
func TestRenderSessionUsage_SubscriptionProvider(t *testing.T) {
	s := &session.Session{
		ID:    "20260528-130000.000000",
		Model: "gpt-5-codex",
		TotalUsage: adapter.Usage{
			InputTokens:  5_000,
			OutputTokens: 1_500,
		},
	}
	profile := adapter.ProviderProfile{
		Provider:               adapter.ProviderOpenAIAuth,
		SupportsUsageReporting: true,
	}
	got := renderSessionUsage(s, profile)

	if !strings.Contains(got, "subscription — no per-request cost") {
		t.Errorf("expected subscription label in:\n%s", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("subscription path must not show a $ figure; got:\n%s", got)
	}
}

// TestRenderSessionUsage_FreeOrLocal covers ollama / NIM: the cost
// line must label them as "free / local" rather than computing a
// dollar number from an absent catalog entry.
func TestRenderSessionUsage_FreeOrLocal(t *testing.T) {
	s := &session.Session{
		ID:    "20260528-140000.000000",
		Model: "qwen3.5:latest",
		TotalUsage: adapter.Usage{
			InputTokens:  10_000,
			OutputTokens: 2_000,
		},
	}
	profile := adapter.ProviderProfile{
		Provider:               adapter.ProviderOllama,
		SupportsUsageReporting: false,
	}
	got := renderSessionUsage(s, profile)

	if !strings.Contains(got, "free / local") {
		t.Errorf("expected free/local label in:\n%s", got)
	}
}

// TestRenderSessionUsage_NoData covers the "session ran but adapter
// never reported usage" path (Ollama on an older OpenAI shim, e.g.).
// The block should call this out explicitly rather than rendering a
// row of zeros.
func TestRenderSessionUsage_NoData(t *testing.T) {
	s := &session.Session{ID: "20260528-150000.000000", Model: "m"}
	profile := adapter.ProviderProfile{Provider: adapter.ProviderAnthropic, SupportsUsageReporting: true}
	got := renderSessionUsage(s, profile)
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
