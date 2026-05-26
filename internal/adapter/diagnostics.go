package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
)

// ProbeResult is the active diagnostics result for one provider config.
type ProbeResult struct {
	Profile           ProviderProfile `json:"profile"`
	BaseURL           string          `json:"base_url"`
	Model             string          `json:"model"`
	HTTPStatus        int             `json:"http_status,omitempty"`
	EndpointReachable bool            `json:"endpoint_reachable"`
	AuthOK            bool            `json:"auth_ok"`
	ModelVisible      bool            `json:"model_visible"`
	AvailableModels   []string        `json:"available_models,omitempty"`
	Issues            []string        `json:"issues,omitempty"`
	Warnings          []string        `json:"warnings,omitempty"`
}

// StaticDiagnostics resolves provider routing and static config diagnostics
// without making any network requests.
func StaticDiagnostics(cfg Config) ProbeResult {
	profile := buildProfile(cfg, usesResponsesAPI(cfg))
	return ProbeResult{
		Profile:  profile,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.Model,
		Issues:   append([]string(nil), profile.Issues...),
		Warnings: append([]string(nil), profile.Warnings...),
	}
}

// Probe runs a lightweight active diagnostics pass against the configured
// provider. It currently validates the /models surface because that is cheap,
// widely available on supported endpoints, and enough to distinguish network,
// auth, and model-visibility failures.
//
// Anthropic is skipped: it does expose /v1/models, but with a different
// auth header shape (`x-api-key` rather than `Authorization: Bearer`)
// that the OpenAI-compatible probe gets wrong. Until we wire a
// dedicated Anthropic probe, the static profile is the diagnostic.
func Probe(ctx context.Context, cfg Config) ProbeResult {
	res := StaticDiagnostics(cfg)
	if strings.TrimSpace(cfg.BaseURL) == "" {
		res.Issues = uniqueStrings(append(res.Issues, "base URL is empty"))
		return res
	}
	if res.Profile.Provider == ProviderAnthropic {
		return res
	}
	// openai-auth has no /models endpoint — its catalog is locked at
	// build time and auth comes from the OAuth token store, not an
	// api_key_env. Probe by checking the token store directly:
	// non-expired access token + listed allow-list = healthy. Doing a
	// real /responses POST would work too but burns ChatGPT
	// subscription quota on every connection check.
	if res.Profile.Provider == ProviderOpenAIAuth {
		return probeOpenAIAuth(res, cfg)
	}
	if res.Profile.Provider == ProviderCopilot {
		return probeCopilot(res, cfg)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+"/models", nil)
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("invalid base URL: %v", err)))
		return res
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("endpoint unreachable: %v", err)))
		return res
	}
	defer resp.Body.Close()

	res.EndpointReachable = true
	res.HTTPStatus = resp.StatusCode

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("authentication failed (HTTP %d)", resp.StatusCode)))
		return res
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("/models probe failed (HTTP %d)", resp.StatusCode)))
		return res
	}

	res.AuthOK = true

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		res.Warnings = uniqueStrings(append(res.Warnings, fmt.Sprintf("could not parse /models response: %v", err)))
		return res
	}

	if len(payload.Data) == 0 {
		res.Warnings = uniqueStrings(append(res.Warnings, "/models returned no models"))
		return res
	}

	res.AvailableModels = make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		res.AvailableModels = append(res.AvailableModels, model.ID)
		if model.ID == cfg.Model {
			res.ModelVisible = true
		}
	}
	if strings.TrimSpace(cfg.Model) != "" && !res.ModelVisible {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("model %q not listed by /models", cfg.Model)))
	}
	return res
}

// probeOpenAIAuth checks the OAuth token store for openai-auth.
// EndpointReachable + AuthOK fold into "have valid token"; the
// available-models list is the static allow-list. No network I/O —
// callers can run this on every focus event without burning quota.
func probeOpenAIAuth(res ProbeResult, cfg Config) ProbeResult {
	storePath, err := openaiauth.DefaultStorePath()
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("openai-auth: resolve token store: %v", err)))
		return res
	}
	ts, err := openaiauth.Load(storePath)
	if err != nil {
		if errors.Is(err, openaiauth.ErrNotFound) {
			res.Issues = uniqueStrings(append(res.Issues, "openai-auth: not logged in — "+openAIAuthLoginHint))
			return res
		}
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("openai-auth: load token store: %v", err)))
		return res
	}
	if ts.AccessToken == "" {
		res.Issues = uniqueStrings(append(res.Issues, "openai-auth: token store has no access token — "+openAIAuthLoginHint))
		return res
	}
	if ts.IsExpired(0) && ts.RefreshToken == "" {
		res.Issues = uniqueStrings(append(res.Issues, "openai-auth: access token expired and no refresh token — "+openAIAuthLoginHint))
		return res
	}
	// All good — token is loaded, accessible, and either fresh or
	// has a refresh_token to recover. Mark the connection healthy.
	res.EndpointReachable = true
	res.AuthOK = true
	allow := supportedModels()
	if len(allow) == 0 {
		res.Issues = uniqueStrings(append(res.Issues,
			"openai-auth: no models discovered yet — "+openAIAuthLoginHint+" to populate the model list"))
		return res
	}
	res.AvailableModels = append([]string(nil), allow...)
	for _, m := range allow {
		if m == cfg.Model {
			res.ModelVisible = true
			break
		}
	}
	if cfg.Model != "" && !res.ModelVisible {
		res.Issues = uniqueStrings(append(res.Issues,
			fmt.Sprintf("model %q not in your account's discovered set (available: %s) — re-run %s to refresh",
				cfg.Model, strings.Join(allow, ", "), openAIAuthLoginHint)))
	}
	return res
}

func probeCopilot(res ProbeResult, cfg Config) ProbeResult {
	storePath, err := copilotauth.DefaultStorePath()
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("copilot: resolve token store: %v", err)))
		return res
	}
	ts, err := copilotauth.Load(storePath)
	if err != nil {
		if errors.Is(err, copilotauth.ErrNotFound) {
			res.Issues = uniqueStrings(append(res.Issues, "copilot: not logged in — "+copilotLoginHint))
			return res
		}
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("copilot: load token store: %v", err)))
		return res
	}
	if ts.GitHubToken == "" {
		res.Issues = uniqueStrings(append(res.Issues, "copilot: token store has no GitHub token — "+copilotLoginHint))
		return res
	}
	res.EndpointReachable = true
	res.AuthOK = true

	modelsPath, err := copilotauth.DefaultModelsPath()
	if err != nil {
		return res
	}
	mf, err := copilotauth.LoadModels(modelsPath)
	if err != nil || len(mf.Models) == 0 {
		return res
	}
	res.AvailableModels = make([]string, 0, len(mf.Models))
	for _, m := range mf.Models {
		res.AvailableModels = append(res.AvailableModels, m.ID)
		if m.ID == cfg.Model {
			res.ModelVisible = true
		}
	}
	return res
}
