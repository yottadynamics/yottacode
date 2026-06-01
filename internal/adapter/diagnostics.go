package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// Anthropic exposes /v1/models like the OpenAI-compatible providers but
// authenticates with x-api-key + anthropic-version headers rather than a
// Bearer token, so it gets a dedicated request shape (probeAnthropic)
// before folding into the shared response handling.
func Probe(ctx context.Context, cfg Config) ProbeResult {
	res := StaticDiagnostics(cfg)
	if strings.TrimSpace(cfg.BaseURL) == "" {
		res.Issues = uniqueStrings(append(res.Issues, "base URL is empty"))
		return res
	}
	// openai-auth has no /models endpoint — its catalog is locked at
	// build time and auth comes from the OAuth token store, not an
	// api_key_env. Probe by checking the token store directly:
	// non-expired access token + listed allow-list = healthy. Doing a
	// real /responses POST would work too but burns ChatGPT
	// subscription quota on every connection check. Copilot is the same
	// token-store shape. Neither does network I/O, so handle them before
	// the HTTP deadline setup.
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

	if res.Profile.Provider == ProviderAnthropic {
		return probeAnthropic(ctx, res, cfg)
	}
	if res.Profile.Provider == ProviderGemini {
		return probeGemini(ctx, res, cfg)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+"/models", nil)
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("invalid base URL: %v", err)))
		return res
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	return runModelsProbe(req, res, cfg, parseOpenAIModels)
}

// probeAnthropic builds the Anthropic /v1/models request and folds the
// response into res via the shared runModelsProbe. Anthropic's base URL
// omits the version segment (the SDK appends /v1/messages itself), so
// /v1/models is built off the bare host; a trailing /v1 on a gateway
// base URL is trimmed first to avoid /v1/v1/models. Mirrors the wizard's
// key-validation header shape (internal/wizard/probe.go).
func probeAnthropic(ctx context.Context, res ProbeResult, cfg Config) ProbeResult {
	base := strings.TrimSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("invalid base URL: %v", err)))
		return res
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		req.Header.Set("x-api-key", cfg.APIKey)
	}
	// Required on every Anthropic REST call; 2023-06-01 is the stable
	// version the wizard's validator also pins.
	req.Header.Set("anthropic-version", "2023-06-01")
	return runModelsProbe(req, res, cfg, parseOpenAIModels)
}

// probeGemini builds the Gemini models request and folds the response
// into res via the shared runModelsProbe. Gemini differs from the
// OpenAI-compatible providers twice over: it authenticates with the
// x-goog-api-key header (not a Bearer token) and its list-models
// response is shaped {"models":[{"name":"models/<id>"}]} rather than
// {"data":[{"id":...}]}. We probe /v1beta/models — the same surface the
// adapter streams against (gemini.go) — so visibility reflects what's
// actually usable; a trailing version segment on a gateway base URL is
// trimmed first.
func probeGemini(ctx context.Context, res ProbeResult, cfg Config) ProbeResult {
	base := strings.TrimRight(cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1beta")
	base = strings.TrimSuffix(base, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1beta/models", nil)
	if err != nil {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("invalid base URL: %v", err)))
		return res
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		req.Header.Set(geminiAPIKeyHeader, cfg.APIKey)
	}
	return runModelsProbe(req, res, cfg, parseGeminiModels)
}

// runModelsProbe executes a prepared list-models request and folds the
// response into res: reachability, auth, the available-model list, and
// model visibility. Shared by the OpenAI-compatible, Anthropic, and
// Gemini paths, which differ in URL shape, auth headers, and response
// body — the body shape is handled by the supplied parseModels.
func runModelsProbe(req *http.Request, res ProbeResult, cfg Config, parseModels func(io.Reader) ([]string, error)) ProbeResult {
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

	models, err := parseModels(resp.Body)
	if err != nil {
		res.Warnings = uniqueStrings(append(res.Warnings, fmt.Sprintf("could not parse /models response: %v", err)))
		return res
	}
	if len(models) == 0 {
		res.Warnings = uniqueStrings(append(res.Warnings, "/models returned no models"))
		return res
	}

	res.AvailableModels = models
	res.ModelVisible = modelListed(models, cfg.Model, res.Profile.Provider)
	if strings.TrimSpace(cfg.Model) != "" && !res.ModelVisible {
		res.Issues = uniqueStrings(append(res.Issues, fmt.Sprintf("model %q not listed by /models", cfg.Model)))
	}
	return res
}

// modelListed reports whether want appears in the provider's available
// list. Matching is exact for every provider except Ollama, which lists
// models with their tag (llama3.2:latest) while configs commonly name the
// bare model (llama3.2). Ollama resolves the implicit :latest at
// generation time, so a working local model would otherwise read amber
// for a tag the user never typed.
func modelListed(available []string, want string, provider Provider) bool {
	for _, id := range available {
		if id == want {
			return true
		}
		if provider == ProviderOllama && withDefaultOllamaTag(id) == withDefaultOllamaTag(want) {
			return true
		}
	}
	return false
}

// withDefaultOllamaTag appends Ollama's implicit :latest tag to a bare
// model name so tagged and untagged forms of the same model compare equal.
func withDefaultOllamaTag(model string) string {
	if strings.ContainsRune(model, ':') {
		return model
	}
	return model + ":latest"
}

// parseOpenAIModels reads the OpenAI-compatible (and Anthropic)
// list-models shape {"data":[{"id":...}]}, returning the non-empty IDs.
func parseOpenAIModels(r io.Reader) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// parseGeminiModels reads the Gemini list-models shape
// {"models":[{"name":"models/<id>"}]}. The bare <id> is what the config
// and streaming path use, so the "models/" prefix is stripped to make
// the visibility comparison line up.
func parseGeminiModels(r io.Reader) ([]string, error) {
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		if id := strings.TrimSpace(strings.TrimPrefix(m.Name, "models/")); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
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
