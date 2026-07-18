package adapter

import (
	"context"
	"fmt"
	"strings"
)

// erroredAdapter is the placeholder adapter returned by NewWithConfig
// when the configuration is statically broken — most commonly a cloud
// provider with no API key. Constructing the real adapter would push
// the failure to the upstream as a confusing "401 Incorrect API key
// provided: local-no-auth" (because we used to substitute that
// sentinel for the SDK's non-empty requirement). This adapter emits a
// clean, actionable error on the first ChatStream so the TUI surface
// reads "[error] openai: no API key configured" instead.
type erroredAdapter struct {
	err     error
	model   string
	profile ProviderProfile
}

func newErroredAdapter(cfg Config, provider Provider, err error) *erroredAdapter {
	return &erroredAdapter{
		err:     err,
		model:   cfg.Model,
		profile: buildProfile(cfg, provider == ProviderOpenAI),
	}
}

func (a *erroredAdapter) Profile() ProviderProfile { return a.profile }
func (a *erroredAdapter) Model() string            { return a.model }

func (a *erroredAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	ch := make(chan StreamEvent, 1)
	go func() {
		defer close(ch)
		// Kind: EventErr matters — consumers (agent loop, TUI,
		// oneshot) switch on Kind, and the zero value
		// (EventTokenDelta) would silently swallow this error.
		ch <- StreamEvent{Kind: EventErr, Err: a.err}
	}()
	return ch
}

// missingAPIKeyError formats the actionable "no API key" message for
// one provider. Single source of truth so /doctor, the picker, and
// the runtime all surface identical wording.
func missingAPIKeyError(provider Provider) error {
	switch provider {
	case ProviderOpenAI:
		return fmt.Errorf("openai: no API key configured — set $OPENAI_API_KEY (or run yottacode setup)")
	case ProviderAnthropic:
		return fmt.Errorf("anthropic: no API key configured — set $ANTHROPIC_API_KEY (or run yottacode setup)")
	case ProviderGemini:
		return fmt.Errorf("gemini: no API key configured — set $GEMINI_API_KEY (or run yottacode setup)")
	case ProviderXAI:
		return fmt.Errorf("xai: no API key configured — set $XAI_API_KEY (or run yottacode setup)")
	default:
		return fmt.Errorf("%s: no API key configured — set the api_key_env var listed in your provider profile", provider)
	}
}

// configRequiresAPIKey reports whether the configured upstream will
// reject requests without auth via cfg.APIKey. Ollama is the canonical
// no-auth case. Loopback URLs (localhost / 127.0.0.1 / ::1) are also
// exempt: they cover self-hosted vLLM, Llama Stack, Ollama on a non-
// default port, and httptest fixtures. openai-auth is exempt because
// auth comes from the OAuth token store, not an api_key_env — its
// cfg.APIKey is always empty by design. Everything else (cloud APIs
// at known hosts, OpenAI-compatible at remote hosts) requires a key.
func configRequiresAPIKey(cfg Config, provider Provider) bool {
	if provider == ProviderOllama {
		return false
	}
	if provider == ProviderOpenAIAuth {
		return false
	}
	if provider == ProviderCopilot {
		return false
	}
	// Vertex authenticates with an Application Default Credentials access
	// token minted per request, not with cfg.APIKey — which is always
	// empty for these kinds by design, exactly like openai-auth.
	if provider == ProviderVertex || provider == ProviderVertexAnthropic {
		return false
	}
	if isLoopbackURL(cfg.BaseURL) {
		return false
	}
	return true
}

// isLoopbackURL is a cheap text test — we don't need full URL parsing
// to recognize developer/test endpoints. Catches every shape httptest
// or self-hosted setups produce in practice.
func isLoopbackURL(baseURL string) bool {
	switch {
	case strings.HasPrefix(baseURL, "http://localhost"),
		strings.HasPrefix(baseURL, "https://localhost"),
		strings.HasPrefix(baseURL, "http://127.0.0.1"),
		strings.HasPrefix(baseURL, "https://127.0.0.1"),
		strings.HasPrefix(baseURL, "http://[::1]"),
		strings.HasPrefix(baseURL, "https://[::1]"):
		return true
	}
	return false
}
