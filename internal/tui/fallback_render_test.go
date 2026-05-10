package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestRenderFallbackLine_ShowsFromToAndPolicy(t *testing.T) {
	got := renderFallbackLine(agent.Fallback{
		From:   "anthropic/claude-haiku-4-5",
		To:     "openai/gpt-4o",
		Reason: "rate limited",
		Policy: "fallback-chain",
	})
	for _, want := range []string{
		"↻ fallback",
		"anthropic/claude-haiku-4-5",
		"openai/gpt-4o",
		"fallback-chain",
		"rate limited",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered fallback line missing %q\nfull: %q", want, got)
		}
	}
}

func TestRenderFallbackLine_OmitsEmptyReason(t *testing.T) {
	got := renderFallbackLine(agent.Fallback{
		From:   "a",
		To:     "b",
		Policy: "fallback-chain",
	})
	if strings.Contains(got, "::") {
		t.Errorf("empty reason should not produce a trailing colon: %q", got)
	}
	for _, want := range []string{"↻ fallback", "a", "b", "fallback-chain"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered fallback line missing %q\nfull: %q", want, got)
		}
	}
}
