package adapter

import (
	"strings"
	"testing"

	"github.com/openai/openai-go/shared"
)

func boolPtr(b bool) *bool { return &b }

func TestOpenAIEffort(t *testing.T) {
	cases := []struct {
		in   string
		want shared.ReasoningEffort
		ok   bool
	}{
		{"low", shared.ReasoningEffortLow, true},
		{"medium", shared.ReasoningEffortMedium, true},
		{"high", shared.ReasoningEffortHigh, true},
		{"  High ", shared.ReasoningEffortHigh, true}, // trimmed + lowercased
		{"", "", false}, // provider default
		{"default", "", false},
		{"none", "", false},
		{"ultra", "", false},
	}
	for _, c := range cases {
		got, ok := openAIEffort(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("openAIEffort(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestXAIEffort(t *testing.T) {
	cases := []struct {
		level string
		model string
		want  shared.ReasoningEffort
		ok    bool
	}{
		{"low", "grok-3-mini", shared.ReasoningEffortLow, true},
		{"medium", "grok-3-mini", shared.ReasoningEffortHigh, true}, // xAI has no medium → fold to high
		{"high", "grok-3-mini-fast", shared.ReasoningEffortHigh, true},
		{"low", "GROK-3-MINI", shared.ReasoningEffortLow, true}, // case-insensitive
		{"high", "grok-4", "", false},                           // grok-4 rejects reasoning_effort
		{"high", "grok-3", "", false},                           // non-mini
		{"high", "gpt-5", "", false},                            // not xAI
		{"", "grok-3-mini", "", false},                          // provider default
	}
	for _, c := range cases {
		got, ok := xaiEffort(c.level, c.model)
		if ok != c.ok || got != c.want {
			t.Errorf("xaiEffort(%q, %q) = (%q, %v), want (%q, %v)", c.level, c.model, got, ok, c.want, c.ok)
		}
	}
}

func TestResponsesReasoningEffort(t *testing.T) {
	cases := []struct {
		level    string
		model    string
		provider Provider
		want     shared.ReasoningEffort
		ok       bool
	}{
		// OpenAI reasoning models use the effort enum directly.
		{"high", "gpt-5", ProviderOpenAI, shared.ReasoningEffortHigh, true},
		{"medium", "o3", ProviderOpenAI, shared.ReasoningEffortMedium, true},
		// OpenAI non-reasoning model → no effort.
		{"high", "gpt-4o", ProviderOpenAI, "", false},
		// xAI dispatches to the mini-gated rule (medium folds to high).
		{"medium", "grok-3-mini", ProviderXAI, shared.ReasoningEffortHigh, true},
		{"high", "grok-4", ProviderXAI, "", false},
		// Empty level always defers to the provider default.
		{"", "gpt-5", ProviderOpenAI, "", false},
	}
	for _, c := range cases {
		got, ok := responsesReasoningEffort(c.level, c.model, c.provider)
		if ok != c.ok || got != c.want {
			t.Errorf("responsesReasoningEffort(%q, %q, %s) = (%q, %v), want (%q, %v)",
				c.level, c.model, c.provider, got, ok, c.want, c.ok)
		}
	}
}

func TestEffortInapplicableReason(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		provider Provider
		wantHas  string // "" means the reason must be empty (effort applies)
	}{
		{"no effort set is never a warning", Config{Model: "gpt-4o"}, ProviderOpenAI, ""},
		{"openai reasoning model applies", Config{Model: "gpt-5", ReasoningEffort: "high"}, ProviderOpenAI, ""},
		{"openai non-reasoning model", Config{Model: "gpt-4o", ReasoningEffort: "high"}, ProviderOpenAI, "isn't a reasoning model"},
		{"codex applies", Config{Model: "gpt-5-codex", ReasoningEffort: "low"}, ProviderOpenAIAuth, ""},
		{"xai mini applies", Config{Model: "grok-3-mini", ReasoningEffort: "high"}, ProviderXAI, ""},
		{"xai grok-4", Config{Model: "grok-4", ReasoningEffort: "high"}, ProviderXAI, "ignores reasoning effort"},
		{"anthropic budgeted", Config{Model: "claude-x", ReasoningEffort: "high", ModelMaxOutput: 64000, ModelSupportsThinking: boolPtr(true)}, ProviderAnthropic, ""},
		{"anthropic no thinking", Config{Model: "claude-x", ReasoningEffort: "high", ModelMaxOutput: 64000, ModelSupportsThinking: boolPtr(false)}, ProviderAnthropic, "doesn't support extended thinking"},
		{"anthropic not in catalog still applies via fallback", Config{Model: "claude-x", ReasoningEffort: "high"}, ProviderAnthropic, ""},
		{"gemini thinking", Config{Model: "gemini-2.5", ReasoningEffort: "high", ModelSupportsThinking: boolPtr(true)}, ProviderGemini, ""},
		{"gemini no thinking", Config{Model: "gemini-1.5", ReasoningEffort: "high", ModelSupportsThinking: boolPtr(false)}, ProviderGemini, "doesn't support thinking"},
		{"copilot not wired", Config{Model: "gpt-5", ReasoningEffort: "high"}, ProviderCopilot, "doesn't apply reasoning effort"},
		{"ollama not wired", Config{Model: "qwen", ReasoningEffort: "high"}, ProviderOllama, "doesn't apply reasoning effort"},
	}
	for _, c := range cases {
		got := EffortInapplicableReason(c.cfg, c.provider)
		switch {
		case c.wantHas == "" && got != "":
			t.Errorf("%s: reason = %q, want empty (effort applies)", c.name, got)
		case c.wantHas != "" && !strings.Contains(got, c.wantHas):
			t.Errorf("%s: reason = %q, want substring %q", c.name, got, c.wantHas)
		}
	}
}

func TestIsXAIMiniModel(t *testing.T) {
	cases := map[string]bool{
		"grok-3-mini":      true,
		"grok-3-mini-fast": true,
		"grok-4":           false,
		"grok-2":           false,
		"gpt-5-mini":       false, // OpenAI, not xAI
		"":                 false,
	}
	for model, want := range cases {
		if got := isXAIMiniModel(model); got != want {
			t.Errorf("isXAIMiniModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestAnthropicThinkingBudget(t *testing.T) {
	cases := []struct {
		name      string
		level     string
		maxOutput int
		thinking  *bool
		want      int64
	}{
		{"provider default (empty level)", "", 64000, boolPtr(true), 0},
		{"explicitly unsupported", "high", 64000, boolPtr(false), 0},
		// Unknown max output (model not in catalog) falls back to the
		// conservative default cap (8192) so effort still engages.
		{"fallback high (uncatalogued)", "high", 0, boolPtr(true), 6144}, // 0.75*8192
		{"fallback low (uncatalogued)", "low", 0, nil, 2048},             // 0.25*8192
		{"unknown support is attempted", "low", 8192, nil, 2048},
		{"low of 8192", "low", 8192, boolPtr(true), 2048},
		{"high of 8192", "high", 8192, boolPtr(true), 6144},
		{"high of 64000", "high", 64000, boolPtr(true), 48000},
		{"floored to 1024", "low", 2048, boolPtr(true), 1024},   // 0.25*2048=512 → floor 1024; ceiling 1024
		{"max output too small", "low", 1500, boolPtr(true), 0}, // ceiling 476 < 1024 → off
	}
	for _, c := range cases {
		if got := anthropicThinkingBudget(c.level, c.maxOutput, c.thinking); got != c.want {
			t.Errorf("%s: anthropicThinkingBudget(%q, %d) = %d, want %d", c.name, c.level, c.maxOutput, got, c.want)
		}
	}
}

func TestAnthropicBudgetNeverReachesMaxOutput(t *testing.T) {
	// The visible answer needs headroom: a budget must always leave at
	// least minThinkingBudgetTokens of max-output room.
	for _, maxOut := range []int{4096, 8192, 16384, 32000, 64000} {
		for _, level := range []string{"low", "medium", "high"} {
			b := anthropicThinkingBudget(level, maxOut, boolPtr(true))
			if b > 0 && b > int64(maxOut)-minThinkingBudgetTokens {
				t.Errorf("budget %d for level=%s maxOut=%d leaves no answer headroom", b, level, maxOut)
			}
		}
	}
}

func TestGeminiThinkingBudget(t *testing.T) {
	cases := []struct {
		name     string
		level    string
		thinking *bool
		want     int64
	}{
		{"provider default", "", boolPtr(true), 0},
		{"explicitly unsupported", "high", boolPtr(false), 0},
		{"low", "low", boolPtr(true), 6144},
		{"medium", "medium", boolPtr(true), 12288},
		{"high", "high", boolPtr(true), 18432},
		{"unknown support is attempted", "high", nil, 18432},
	}
	for _, c := range cases {
		if got := geminiThinkingBudget(c.level, c.thinking); got != c.want {
			t.Errorf("%s: geminiThinkingBudget(%q) = %d, want %d", c.name, c.level, got, c.want)
		}
	}
	// Every derived budget must stay within the Gemini 2.5 family range.
	for _, level := range []string{"low", "medium", "high"} {
		if b := geminiThinkingBudget(level, nil); b < 0 || b > geminiMaxThinkingBudget {
			t.Errorf("gemini budget %d for level=%s out of range [0,%d]", b, level, geminiMaxThinkingBudget)
		}
	}
}
