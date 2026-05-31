package adapter

import (
	"context"
	"strings"
)

// Streamer is the slice of behavior every concrete adapter implements. Kept
// mirrored with agent.Streamer so the agent loop can take any adapter the
// router hands it.
type Streamer interface {
	ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent
}

// New returns a Client wired to the provider implied by baseURL + model.
// Kept for compatibility with the original constructor shape.
func New(baseURL, apiKey, model string) Client {
	return NewWithConfig(Config{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	})
}

// NewWithConfig returns a Client wired to the provider implied by the config.
//
// Routing rules:
//
//   - api.openai.com + (o1*, o3*, o4*, gpt-5*) → Responses API
//     (the only way to surface reasoning summaries for OpenAI's reasoning
//     models — Chat Completions hides them.)
//   - api.openai.com + Responses-only built-in tools enabled
//     (`web_search`, `code_interpreter`) → Responses API
//   - everything else → OpenAI-compatible Chat Completions
//     (Ollama, Llama Stack, vLLM, xAI, Together, OpenRouter, NVIDIA NIM,
//     Groq, real OpenAI for non-reasoning models like gpt-4o / gpt-4.1.)
//
// Auto-detection covers ~all real-world cases. There is no flag override —
// if you point at api.openai.com with an o-series or gpt-5 model, you get
// reasoning streaming for free.
func NewWithConfig(cfg Config) Client {
	provider := detectProvider(cfg.BaseURL, cfg.ProviderOverride)
	// openai-auth must branch BEFORE the API-key check: this kind
	// authenticates via a bearer token loaded from the OAuth store,
	// not from cfg.APIKey, so the configRequiresAPIKey path would
	// otherwise reject a perfectly valid configuration.
	if provider == ProviderOpenAIAuth {
		return newOpenAIAuthAdapter(cfg)
	}
	if provider == ProviderCopilot {
		return newCopilotAdapter(cfg)
	}
	// Static configuration check: cloud providers all need an API key,
	// and silently substituting the "local-no-auth" sentinel just
	// pushes the failure upstream as a confusing 401. Fail fast with
	// an actionable message instead.
	if cfg.APIKey == "" && configRequiresAPIKey(cfg, provider) {
		return newErroredAdapter(cfg, provider, missingAPIKeyError(provider))
	}
	if provider == ProviderAnthropic || isAnthropicModel(cfg.Model) {
		return newAnthropicAdapter(cfg)
	}
	if provider == ProviderGemini || isGeminiModel(cfg.Model) {
		return newGeminiAdapter(cfg)
	}
	if usesResponsesAPIWithProvider(cfg, provider) {
		return newResponsesAdapter(cfg)
	}
	return newChatAdapter(cfg)
}

// isAnthropicModel triggers the Anthropic adapter for any claude-*
// model even when the base URL doesn't say so explicitly. Real-world
// configurations sometimes proxy Anthropic through a corporate gateway
// at a custom hostname; the model tag is the strongest signal of what
// API shape the upstream actually speaks.
func isAnthropicModel(model string) bool {
	return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "claude_")
}

// isGeminiModel triggers the Gemini adapter for any gemini-* model,
// matching the Anthropic strategy: when a corporate proxy fronts the
// Google API the base URL won't say "generativelanguage.googleapis.com"
// but the model tag still does.
func isGeminiModel(model string) bool {
	return strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "gemini_")
}

// usesResponsesAPI is the routing + diagnostics predicate. Conservative:
// only triggers for hosts and providers we know speak the Responses API.
//
//   - openai-auth always speaks Responses (the Codex backend's only
//     surface) — return true unconditionally so static diagnostics show
//     the right shape even before any network probe runs.
//   - api.openai.com routes via Responses for o-series / gpt-5* (the
//     reasoning-streaming path) or when a Responses-only built-in tool
//     is requested.
//   - xAI routes via Responses when its built-in tools are enabled.
//   - everything else (self-hosted gpt-5 behind a proxy, etc.) stays
//     on chat-completions.
func usesResponsesAPI(cfg Config) bool {
	return usesResponsesAPIWithProvider(cfg, detectProvider(cfg.BaseURL, cfg.ProviderOverride))
}

// usesResponsesAPIWithProvider is the body of usesResponsesAPI for callers that
// have already resolved the provider — NewWithConfig passes its result through
// so detectProvider runs once per construction instead of twice.
func usesResponsesAPIWithProvider(cfg Config, provider Provider) bool {
	if provider == ProviderOpenAIAuth {
		return true
	}
	if provider != ProviderOpenAI && provider != ProviderXAI {
		return false
	}
	if provider == ProviderOpenAI && isReasoningModel(cfg.Model) {
		return true
	}
	return effectiveWebSearchEnabled(cfg, provider) || cfg.EnableXSearch || cfg.EnableCodeInterpreter
}
