package adapter

import "strings"

// Provider identifies the upstream model vendor or compatibility family that
// yottacode believes it is talking to.
type Provider string

const (
	ProviderOpenAI           Provider = "openai"
	ProviderOpenAIAuth       Provider = "openai-auth"
	ProviderCopilot          Provider = "copilot"
	ProviderXAI              Provider = "xai"
	ProviderOllama           Provider = "ollama"
	ProviderAnthropic        Provider = "anthropic"
	ProviderGemini           Provider = "gemini"
	ProviderOpenAICompatible Provider = "openai-compatible"
	// ProviderVertex and ProviderVertexAnthropic are Google Vertex AI:
	// Gemini and Claude served from the user's own GCP project. They are
	// separate providers from gemini/anthropic because the credential is
	// a ~1h Application Default Credentials access token rather than an
	// API key — the same reason openai-auth and copilot are separate from
	// openai. They are separate from *each other* because Vertex serves
	// the two model families over different surfaces: Gemini via an
	// OpenAI-compatible chat shim, Claude via :streamRawPredict speaking
	// the native Anthropic Messages API.
	ProviderVertex          Provider = "vertex"
	ProviderVertexAnthropic Provider = "vertex-anthropic"
)

// BuiltinToolKind is a provider-native capability exposed directly by the
// model provider rather than by yottacode's local tool registry.
type BuiltinToolKind string

const (
	BuiltinToolWebSearch       BuiltinToolKind = "web_search"
	BuiltinToolXSearch         BuiltinToolKind = "x_search"
	BuiltinToolCodeInterpreter BuiltinToolKind = "code_interpreter"
)

// Config configures adapter construction and provider-native capabilities.
type Config struct {
	BaseURL          string
	APIKey           string
	Model            string
	ProviderOverride Provider
	ReasoningEffort  string
	// CacheKey is the stable per-conversation identifier sent as OpenAI's
	// `prompt_cache_key`. It pins every turn of a session to the same
	// server-side prompt-cache shard so the (byte-identical) prompt prefix
	// keeps hitting the KV cache instead of oscillating across
	// load-balanced shards — the same lever the official Codex CLI pulls,
	// which sets it to the conversation id. Populated from
	// session.Session.ID at construction. Empty omits the field, so a
	// session-less caller (e.g. the connection probe) sends the same
	// request shape as before. Consumed by real OpenAI adapters only:
	// OpenAI Responses, openai-auth, and api.openai.com Chat Completions.
	// OpenAI-compatible providers intentionally do not receive it because
	// some reject unknown request fields.
	CacheKey string
	// ModelMaxOutput and ModelSupportsThinking carry catalog-derived
	// facts about the active model so budget-based reasoning providers
	// (Anthropic, Gemini) can size a thinking budget without the adapter
	// package importing catalog (which would cycle:
	// adapter → catalog → auth/openai → adapter). Callers that have the
	// catalog handy fill these from catalog.FindByID; both are
	// zero/nil-safe — an unknown model leaves reasoning at the provider
	// default. ModelSupportsThinking is a tristate: nil = unknown.
	ModelMaxOutput         int
	ModelSupportsThinking  *bool
	EnableWebSearch        bool
	DisableWebSearch       bool
	EnableXSearch          bool
	EnableCodeInterpreter  bool
	SearchAllowedDomains   []string
	SearchExcludedDomains  []string
	XSearchAllowedHandles  []string
	XSearchExcludedHandles []string
	XSearchFromDate        string
	XSearchToDate          string
}

// ProviderProfile is the resolved view of provider capabilities for a given
// adapter instance.
type ProviderProfile struct {
	Provider                Provider `json:"provider"`
	UsesResponsesAPI        bool     `json:"uses_responses_api"`
	SupportsReasoning       bool     `json:"supports_reasoning"`
	SupportsImages          bool     `json:"supports_images"`
	SupportsWebSearch       bool     `json:"supports_web_search"`
	SupportsXSearch         bool     `json:"supports_x_search"`
	SupportsCodeInterpreter bool     `json:"supports_code_interpreter"`
	// SupportsUsageReporting indicates the adapter populates per-turn
	// Usage on its returned Message. False for local/free providers
	// (Ollama, NVIDIA NIM) where /usage has no meaningful surface.
	SupportsUsageReporting bool              `json:"supports_usage_reporting"`
	EnabledBuiltinTools    []BuiltinToolKind `json:"enabled_builtin_tools,omitempty"`
	Issues                 []string          `json:"issues,omitempty"`
	Warnings               []string          `json:"warnings,omitempty"`
}

// Client is the richer adapter surface used by entry points. The agent loop
// still only depends on ChatStream; Profile is for UI, diagnostics, and
// future routing decisions.
type Client interface {
	Streamer
	Profile() ProviderProfile
}

func detectProvider(baseURL string, override Provider) Provider {
	if override != "" {
		return override
	}
	switch {
	case strings.Contains(baseURL, "chatgpt.com/backend-api/codex"):
		// The Codex backend reachable via OAuth (Sign-in-with-ChatGPT)
		// auth — distinct from api.openai.com both in URL and in
		// request contract. Detect it BEFORE api.openai.com because
		// the patterns are disjoint anyway, but listing chatgpt.com
		// first keeps the common case (openai) at the same speed.
		return ProviderOpenAIAuth
	case strings.Contains(baseURL, "api.githubcopilot.com"):
		return ProviderCopilot
	case strings.Contains(baseURL, "api.openai.com"):
		return ProviderOpenAI
	case strings.Contains(baseURL, "api.anthropic.com"):
		return ProviderAnthropic
	case strings.Contains(baseURL, "api.x.ai"):
		return ProviderXAI
	case strings.Contains(baseURL, "generativelanguage.googleapis.com"):
		return ProviderGemini
	case strings.Contains(baseURL, "aiplatform.googleapis.com"):
		// Both Vertex kinds live on this host, so the host alone can't
		// tell them apart — the path can. The OpenAI-compatible chat
		// shim is mounted at .../endpoints/openapi and serves Gemini
		// only; anything else under aiplatform is the publisher-model
		// surface, which for us means Claude via :streamRawPredict.
		// A config.toml `kind` arrives as ProviderOverride and short-
		// circuits this above, so this only has to carry the env-var
		// (YOTTACODE_BASE_URL) case.
		if strings.Contains(baseURL, "/endpoints/openapi") {
			return ProviderVertex
		}
		return ProviderVertexAnthropic
	case strings.Contains(baseURL, "localhost:11434"),
		strings.Contains(baseURL, "127.0.0.1:11434"),
		strings.Contains(baseURL, "ollama"):
		return ProviderOllama
	default:
		return ProviderOpenAICompatible
	}
}

// resolveProvider is the single source of truth for "which provider does
// this config actually talk to." It layers the model-tag fallback on top
// of the base-URL guess: a claude-*/gemini-* model is strong enough
// evidence to override the URL, because corporate proxies front those
// APIs at custom hostnames where detectProvider would otherwise guess
// openai-compatible. Both the adapter router and the diagnostics/profile
// path go through here so the connection probe can never disagree with
// the adapter that was actually constructed.
func resolveProvider(cfg Config) Provider {
	provider := detectProvider(cfg.BaseURL, cfg.ProviderOverride)
	switch {
	case provider == ProviderVertex || provider == ProviderVertexAnthropic:
		// Vertex must win before the model-tag fallback below, which is
		// otherwise unconditional. Vertex serves claude-*/gemini-* models
		// under Google's own auth and URLs, so letting the model tag
		// decide would silently reroute a Vertex config to
		// api.anthropic.com / generativelanguage.googleapis.com with a
		// credential those hosts reject. The tag is evidence about the
		// API *shape*; here we already know the shape AND the host.
		return provider
	case provider == ProviderAnthropic || isAnthropicModel(cfg.Model):
		return ProviderAnthropic
	case provider == ProviderGemini || isGeminiModel(cfg.Model):
		return ProviderGemini
	default:
		return provider
	}
}

func isReasoningModel(model string) bool {
	switch {
	case strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"),
		strings.HasPrefix(model, "gpt-5"):
		return true
	}
	return false
}

func enabledBuiltinTools(cfg Config, provider Provider) []BuiltinToolKind {
	var out []BuiltinToolKind
	if effectiveWebSearchEnabled(cfg, provider) {
		out = append(out, BuiltinToolWebSearch)
	}
	if provider == ProviderXAI {
		out = append(out, BuiltinToolXSearch)
	}
	if (provider == ProviderOpenAI || provider == ProviderXAI) && cfg.EnableCodeInterpreter {
		out = append(out, BuiltinToolCodeInterpreter)
	}
	return out
}

// effectiveWebSearchEnabled decides whether the resolved profile
// should advertise the web_search builtin to the model.
//
//   - OpenAI / xAI: default-on (existing behavior; hosted endpoints
//     ship the tool at no extra opt-in friction)
//   - Anthropic:    default-off, requires explicit --enable-web-search.
//     Anthropic bills web_search per call and the feature is in beta;
//     a surprise charge is a worse failure than "tool not enabled"
//   - Anything else: never enabled
func effectiveWebSearchEnabled(cfg Config, provider Provider) bool {
	if cfg.DisableWebSearch {
		return false
	}
	switch provider {
	case ProviderOpenAI, ProviderXAI:
		return true
	case ProviderAnthropic:
		return cfg.EnableWebSearch
	default:
		return false
	}
}

func buildProfile(cfg Config, usesResponses bool) ProviderProfile {
	provider := resolveProvider(cfg)
	enabled := enabledBuiltinTools(cfg, provider)
	profile := ProviderProfile{
		Provider:                provider,
		UsesResponsesAPI:        usesResponses,
		SupportsReasoning:       provider == ProviderOpenAI || provider == ProviderOpenAIAuth || provider == ProviderCopilot || provider == ProviderXAI || provider == ProviderOllama || provider == ProviderAnthropic || provider == ProviderGemini || provider == ProviderVertex || provider == ProviderVertexAnthropic,
		SupportsImages:          provider == ProviderAnthropic || provider == ProviderOpenAI || provider == ProviderOpenAIAuth || provider == ProviderCopilot || provider == ProviderGemini || provider == ProviderXAI || provider == ProviderVertex || provider == ProviderVertexAnthropic,
		SupportsWebSearch:       provider == ProviderOpenAI || provider == ProviderXAI || provider == ProviderAnthropic,
		SupportsXSearch:         provider == ProviderXAI,
		SupportsCodeInterpreter: provider == ProviderOpenAI || provider == ProviderXAI,
		SupportsUsageReporting:  !isFreeOrLocal(provider, cfg.BaseURL),
		EnabledBuiltinTools:     enabled,
	}
	profile.Issues, profile.Warnings = providerDiagnostics(cfg, profile)
	return profile
}

func providerDiagnostics(cfg Config, profile ProviderProfile) (issues, warnings []string) {
	requested := requestedBuiltinTools(cfg)
	for _, tool := range requested {
		if !hasBuiltinTool(profile.EnabledBuiltinTools, tool) {
			issues = append(issues, unsupportedBuiltinToolMessage(tool, profile.Provider))
		}
	}

	if len(cfg.SearchAllowedDomains) > 0 && len(cfg.SearchExcludedDomains) > 0 {
		issues = append(issues, "search allowed/excluded domains cannot both be set")
	}
	if len(cfg.XSearchAllowedHandles) > 0 && len(cfg.XSearchExcludedHandles) > 0 {
		issues = append(issues, "x_search allowed/excluded handles cannot both be set")
	}
	if profile.Provider != ProviderXAI {
		if len(cfg.XSearchAllowedHandles) > 0 || len(cfg.XSearchExcludedHandles) > 0 || cfg.XSearchFromDate != "" || cfg.XSearchToDate != "" {
			issues = append(issues, "x_search filters require the xAI provider")
		}
	}
	// configRequiresAPIKey is the single source of truth for "does
	// this provider need cfg.APIKey set?" — keeps the static
	// dispatch check and this warning in lockstep, and exempts
	// openai-auth (which authenticates via the OAuth token store).
	if configRequiresAPIKey(cfg, profile.Provider) && strings.TrimSpace(cfg.APIKey) == "" {
		warnings = append(warnings, "API key is empty for a remote provider")
	}
	switch {
	case profile.Provider == ProviderOpenAI && looksLikeXAIModel(cfg.Model):
		warnings = append(warnings, "model name looks like xAI/Grok, but provider is openai")
	case profile.Provider == ProviderXAI && looksLikeOpenAIModel(cfg.Model):
		warnings = append(warnings, "model name looks like OpenAI, but provider is xai")
	case profile.Provider == ProviderOllama && (looksLikeOpenAIModel(cfg.Model) || looksLikeXAIModel(cfg.Model)):
		warnings = append(warnings, "model name looks hosted; ensure the Ollama endpoint exposes a matching local alias")
	}
	return uniqueStrings(issues), uniqueStrings(warnings)
}

func requestedBuiltinTools(cfg Config) []BuiltinToolKind {
	var out []BuiltinToolKind
	if cfg.EnableWebSearch {
		out = append(out, BuiltinToolWebSearch)
	}
	if cfg.EnableXSearch {
		out = append(out, BuiltinToolXSearch)
	}
	if cfg.EnableCodeInterpreter {
		out = append(out, BuiltinToolCodeInterpreter)
	}
	return out
}

func hasBuiltinTool(tools []BuiltinToolKind, want BuiltinToolKind) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func unsupportedBuiltinToolMessage(tool BuiltinToolKind, provider Provider) string {
	switch tool {
	case BuiltinToolXSearch:
		return "x_search is only available for the xAI provider"
	case BuiltinToolWebSearch:
		if provider == ProviderOpenAICompatible || provider == ProviderOllama {
			return "web_search requires OpenAI, xAI, or Anthropic provider-native support"
		}
	case BuiltinToolCodeInterpreter:
		if provider == ProviderOpenAICompatible || provider == ProviderOllama || provider == ProviderAnthropic {
			return "code_interpreter requires OpenAI or xAI provider-native support"
		}
	}
	return string(tool) + " is not supported by the selected provider"
}

func remoteProvider(provider Provider, baseURL string) bool {
	if provider == ProviderOllama {
		return false
	}
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	return !strings.Contains(baseURL, "localhost") && !strings.Contains(baseURL, "127.0.0.1")
}

// isFreeOrLocal flags providers that don't bill per token and so have
// no meaningful surface for /usage. Ollama is the obvious local case.
// NVIDIA NIM (served via openai-compatible at integrate.api.nvidia.com)
// is in the per-user-decision exclusion list — Inception credits make
// the per-call cost undefined for end users. Any openai-compatible
// proxy pointed at localhost / 127.0.0.1 is treated as local too,
// covering self-hosted vLLM and similar.
func isFreeOrLocal(provider Provider, baseURL string) bool {
	if provider == ProviderOllama {
		return true
	}
	if provider != ProviderOpenAICompatible {
		return false
	}
	b := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(b, "integrate.api.nvidia.com") ||
		strings.Contains(b, "localhost") ||
		strings.Contains(b, "127.0.0.1")
}

func looksLikeOpenAIModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4")
}

func looksLikeXAIModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "grok")
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
