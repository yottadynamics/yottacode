package adapter

import (
	"fmt"
	"strings"

	"github.com/openai/openai-go/shared"
)

// reasoning.go is the single place that translates yottacode's uniform
// effort surface — "" (provider default), "low", "medium", "high" — onto
// each provider's concrete reasoning knob. Upstream exposes two shapes:
//
//   - an effort enum (OpenAI, xAI): map the level straight onto the
//     vendored SDK's own constant. The set of valid values is part of
//     the provider's API contract carried by the SDK, not a list we
//     hand-maintain.
//   - a thinking-token budget (Anthropic, Gemini): there is no effort
//     enum, so we derive a budget from the level. Anthropic's budget
//     scales with the model's max-output tokens (sourced from the
//     catalog), so it tracks whatever the provider reports and needs no
//     per-model table. Gemini's thinking budget has its own model-
//     specific range the list API does not surface, so we scale a
//     single documented ceiling instead.
//
// The only genuinely yottacode-owned values here — the level names and
// the budget fractions — are product decisions no API returns.

// openAIEffort maps the uniform level onto OpenAI's reasoning.effort
// enum (Responses API + Codex backend). ok is false for "" (leave the
// API at its default — inject nothing) or an unrecognized level.
func openAIEffort(level string) (shared.ReasoningEffort, bool) {
	switch normalizeEffort(level) {
	case "low":
		return shared.ReasoningEffortLow, true
	case "medium":
		return shared.ReasoningEffortMedium, true
	case "high":
		return shared.ReasoningEffortHigh, true
	}
	return "", false
}

// responsesReasoningEffort picks the effort enum for the Responses-API
// adapter, which serves OpenAI reasoning models and — when built-in
// tools (web_search/x_search) route them there — xAI grok models too.
// Dispatches to the right per-provider rule so a single call site in
// responses.go covers both. ok is false to leave the API at its default.
func responsesReasoningEffort(level, model string, provider Provider) (shared.ReasoningEffort, bool) {
	if provider == ProviderXAI {
		return xaiEffort(level, model)
	}
	if isReasoningModel(model) {
		return openAIEffort(level)
	}
	return "", false
}

// xaiEffort maps the uniform level onto xAI's reasoning_effort. xAI
// accepts only "low" and "high", and only on the grok-*-mini family —
// grok-4 reasons unconditionally and the API rejects the parameter, so
// we gate on the model name and fold "medium" up to "high". ok is false
// for an empty/unknown level or a non-mini model.
func xaiEffort(level, model string) (shared.ReasoningEffort, bool) {
	if !isXAIMiniModel(model) {
		return "", false
	}
	switch normalizeEffort(level) {
	case "low":
		return shared.ReasoningEffortLow, true
	case "medium", "high":
		return shared.ReasoningEffortHigh, true
	}
	return "", false
}

// isXAIMiniModel reports whether the model is an xAI grok mini variant
// (grok-3-mini, grok-3-mini-fast, …) — the only xAI models that accept
// reasoning_effort.
func isXAIMiniModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return looksLikeXAIModel(m) && strings.Contains(m, "mini")
}

// minThinkingBudgetTokens is Anthropic's documented floor for an enabled
// thinking block. Gemini's accepted minimum is lower, so this floor is
// safe for both.
const minThinkingBudgetTokens = 1024

// geminiMaxThinkingBudget is the largest thinking budget valid across
// the Gemini 2.5 family (Flash caps at 24576; Pro allows more). Using
// the smallest family maximum keeps any derived budget in range for
// every 2.5 model without a per-model table. Gemini's list endpoint
// reports output-token limits but not the thinking-budget range, so
// this is the one constant we can't source from the catalog.
const geminiMaxThinkingBudget = 24576

// thinkingBudgetFraction is the single policy knob shared by both
// budget-based providers: the share of the available headroom each
// level devotes to thinking. Not per-model — it scales with whatever
// ceiling the caller applies. ok is false for "" or an unknown level.
func thinkingBudgetFraction(level string) (float64, bool) {
	switch normalizeEffort(level) {
	case "low":
		return 0.25, true
	case "medium":
		return 0.50, true
	case "high":
		return 0.75, true
	}
	return 0, false
}

// anthropicThinkingBudget derives an extended-thinking budget (in
// tokens) from the level and the model's max-output tokens. Returns 0 —
// meaning "leave thinking off, at the provider default" — when the level
// is empty/unknown or the catalog explicitly reports the model can't
// think.
//
// When max output is unknown (the model isn't in the catalog — common,
// since the catalog lags new releases), it falls back to the
// conservative AnthropicDefaultMaxTokens cap rather than no-op'ing, so
// effort still engages on any thinking-capable Claude — just with a
// smaller budget than a catalogued, model-scaled one. The budget is
// clamped to leave at least minThinkingBudgetTokens of headroom for the
// visible answer, since Anthropic's max_tokens covers thinking + output
// together.
func anthropicThinkingBudget(level string, maxOutput int, supportsThinking *bool) int64 {
	if thinkingExplicitlyUnsupported(supportsThinking) {
		return 0
	}
	frac, ok := thinkingBudgetFraction(level)
	if !ok {
		return 0
	}
	if maxOutput <= 0 {
		maxOutput = int(AnthropicDefaultMaxTokens)
	}
	ceiling := int64(maxOutput) - minThinkingBudgetTokens
	if ceiling < minThinkingBudgetTokens {
		return 0 // max output too small to host thinking + an answer
	}
	budget := int64(float64(maxOutput) * frac)
	if budget < minThinkingBudgetTokens {
		budget = minThinkingBudgetTokens
	}
	if budget > ceiling {
		budget = ceiling
	}
	return budget
}

// geminiThinkingBudget derives a Gemini thinkingBudget (in tokens) from
// the level. Gemini's per-model budget range isn't in the catalog, so
// we scale geminiMaxThinkingBudget rather than the model's output limit.
// Returns 0 to leave thinking at the provider default when the level is
// empty/unknown or the catalog reports the model can't think.
func geminiThinkingBudget(level string, supportsThinking *bool) int64 {
	if thinkingExplicitlyUnsupported(supportsThinking) {
		return 0
	}
	frac, ok := thinkingBudgetFraction(level)
	if !ok {
		return 0
	}
	return int64(float64(geminiMaxThinkingBudget) * frac)
}

// thinkingExplicitlyUnsupported reports whether the catalog told us, in
// no uncertain terms, that the model cannot think. nil (unknown) is
// treated as "go ahead and try" — most current Anthropic/Gemini models
// support thinking, and silently dropping an explicit effort request is
// the worse failure. Only an explicit false suppresses reasoning.
func thinkingExplicitlyUnsupported(supportsThinking *bool) bool {
	return supportsThinking != nil && !*supportsThinking
}

func normalizeEffort(level string) string {
	return strings.ToLower(strings.TrimSpace(level))
}

// EffortInapplicableReason explains, in one user-facing phrase, why the
// configured reasoning effort will NOT take effect for this
// provider+model — or "" when it will (or when no effort is set, which
// is never a no-op worth warning about). Mirrors exactly the gating each
// adapter applies, so the TUI can warn that a setting is silently a
// no-op on the active model instead of leaving the user guessing.
func EffortInapplicableReason(cfg Config, provider Provider) string {
	if strings.TrimSpace(cfg.ReasoningEffort) == "" {
		return ""
	}
	model := cfg.Model
	switch provider {
	case ProviderOpenAI, ProviderOpenAIAuth:
		if !isReasoningModel(model) {
			return fmt.Sprintf("%s isn't a reasoning model — effort applies only to o-series and gpt-5 models", model)
		}
	case ProviderXAI:
		if !isXAIMiniModel(model) {
			return fmt.Sprintf("%s ignores reasoning effort — only grok-*-mini models accept it", model)
		}
	case ProviderAnthropic:
		// A model not in the catalog still gets a (conservative) budget
		// via the fallback, so the only true no-op is an explicit
		// can't-think from the catalog.
		if thinkingExplicitlyUnsupported(cfg.ModelSupportsThinking) {
			return fmt.Sprintf("%s doesn't support extended thinking", model)
		}
	case ProviderGemini:
		if geminiThinkingBudget(cfg.ReasoningEffort, cfg.ModelSupportsThinking) == 0 {
			return fmt.Sprintf("%s doesn't support thinking", model)
		}
	default:
		return "the active provider doesn't apply reasoning effort"
	}
	return ""
}
