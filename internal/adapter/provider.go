package adapter

import "strings"

// Provider identifies the upstream model vendor or compatibility family that
// yottacode believes it is talking to.
type Provider string

const (
	ProviderOpenAI           Provider = "openai"
	ProviderOpenAIAuth       Provider = "openai-auth"
	ProviderXAI              Provider = "xai"
	ProviderOllama           Provider = "ollama"
	ProviderAnthropic        Provider = "anthropic"
	ProviderGemini           Provider = "gemini"
	ProviderOpenAICompatible Provider = "openai-compatible"
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
	BaseURL                string
	APIKey                 string
	Model                  string
	ProviderOverride       Provider
	ReasoningEffort        string
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
	Provider                Provider          `json:"provider"`
	UsesResponsesAPI        bool              `json:"uses_responses_api"`
	SupportsReasoning       bool              `json:"supports_reasoning"`
	SupportsWebSearch       bool              `json:"supports_web_search"`
	SupportsXSearch         bool              `json:"supports_x_search"`
	SupportsCodeInterpreter bool              `json:"supports_code_interpreter"`
	EnabledBuiltinTools     []BuiltinToolKind `json:"enabled_builtin_tools,omitempty"`
	Issues                  []string          `json:"issues,omitempty"`
	Warnings                []string          `json:"warnings,omitempty"`
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
	case strings.Contains(baseURL, "api.openai.com"):
		return ProviderOpenAI
	case strings.Contains(baseURL, "api.anthropic.com"):
		return ProviderAnthropic
	case strings.Contains(baseURL, "api.x.ai"):
		return ProviderXAI
	case strings.Contains(baseURL, "generativelanguage.googleapis.com"):
		return ProviderGemini
	case strings.Contains(baseURL, "localhost:11434"),
		strings.Contains(baseURL, "127.0.0.1:11434"),
		strings.Contains(baseURL, "ollama"):
		return ProviderOllama
	default:
		return ProviderOpenAICompatible
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
	if provider == ProviderXAI && cfg.EnableXSearch {
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
	provider := detectProvider(cfg.BaseURL, cfg.ProviderOverride)
	enabled := enabledBuiltinTools(cfg, provider)
	profile := ProviderProfile{
		Provider:                provider,
		UsesResponsesAPI:        usesResponses,
		SupportsReasoning:       provider == ProviderOpenAI || provider == ProviderOpenAIAuth || provider == ProviderXAI || provider == ProviderOllama || provider == ProviderAnthropic || provider == ProviderGemini,
		SupportsWebSearch:       provider == ProviderOpenAI || provider == ProviderXAI || provider == ProviderAnthropic,
		SupportsXSearch:         provider == ProviderXAI,
		SupportsCodeInterpreter: provider == ProviderOpenAI || provider == ProviderXAI,
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
