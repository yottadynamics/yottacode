package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DefaultCodexEndpoint is the ChatGPT-account Responses-API URL used
// both for real model calls and, here, to verify each catalog
// candidate is actually usable by this account (some are gated by
// plan tier or trust level in ways the catalog's generic metadata
// doesn't reflect). Distinct from api.openai.com/v1/responses — see
// the comment on the adapter's OpenAIAuthEndpoint for the per-request
// contract differences.
const DefaultCodexEndpoint = "https://chatgpt.com/backend-api/codex/responses"

// ModelsEndpoint is the ChatGPT-account model catalog endpoint — the
// same GET the official Codex CLI's model picker reads to populate
// its list. Traced from OpenAI's open-source codex-rs
// (codex-rs/codex-api/src/endpoint/models.rs: request_url =
// "<base>/models?client_version=..."). It supplies the *candidate*
// list — which slugs exist and are API-shaped at all — replacing the
// maintainer-curated guess list this package used to carry. It does
// NOT by itself confirm this account can call a given slug (see
// ScanWithToken, which probes each candidate against
// DefaultCodexEndpoint for that).
//
// Undocumented by OpenAI, same as the /backend-api/me probe in
// adapter/openai_auth.go: same backend, same bearer token, same
// "degrade gracefully if the shape drifts" contract.
const ModelsEndpoint = "https://chatgpt.com/backend-api/codex/models"

// modelsClientVersion is sent as the `client_version` query parameter
// the backend uses to gate newer models behind a minimum Codex CLI
// release. Bump this if a model visible in the real Codex CLI's
// picker stops appearing in FetchModels' results — that means OpenAI
// raised a model's minimum required client version past this string.
const modelsClientVersion = "0.147.0"

// ErrNoModelsAvailable is returned by Scan / ScanAndPersist when the
// scan ran end-to-end but every candidate was rejected. Distinct from
// network errors so callers can render a more specific message
// ("your account may not have access to any of the candidates").
var ErrNoModelsAvailable = errors.New("openai-auth: scan returned zero usable models")

// FetchedModel is one catalog candidate: API-shaped and listed, but
// not yet confirmed usable by this account. ScanWithToken turns these
// into ScanResults by probing each one.
type FetchedModel struct {
	Slug        string
	DisplayName string
}

// remoteModel mirrors the subset of fields codex-protocol's ModelInfo
// (openai/codex, codex-rs/protocol/src/openai_models.rs) that this
// package needs. Unrecognised upstream fields are ignored by
// json.Unmarshal, so new ones appearing there is not a breaking
// change here.
type remoteModel struct {
	Slug           string `json:"slug"`
	DisplayName    string `json:"display_name"`
	Visibility     string `json:"visibility"`
	SupportedInAPI bool   `json:"supported_in_api"`
	Priority       int    `json:"priority"`
}

type modelsCatalogResponse struct {
	Models []remoteModel `json:"models"`
}

// ScanOptions tunes Scan / ScanWithToken / FetchModels. Zero values
// pick the production defaults so callers can `Scan(ctx, path,
// ScanOptions{})` with no boilerplate.
type ScanOptions struct {
	HTTPClient *http.Client
	// ModelsEndpoint is the catalog GET endpoint (candidate discovery).
	ModelsEndpoint string
	// ProbeEndpoint is the Responses POST endpoint each candidate is
	// verified against (per-account confirmation).
	ProbeEndpoint string
	Originator    string
}

func (o *ScanOptions) defaults() {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.ModelsEndpoint == "" {
		o.ModelsEndpoint = ModelsEndpoint
	}
	if o.ProbeEndpoint == "" {
		o.ProbeEndpoint = DefaultCodexEndpoint
	}
	if o.Originator == "" {
		o.Originator = DefaultOriginator
	}
}

// FetchModels GETs the live model catalog and returns the candidates
// worth probing, ordered by the backend's priority (best default
// first).
//
// Filters out visibility=="hide" (internal routing aliases like
// gpt-5.6-sol-wm, and the review-only codex-auto-review) and
// supported_in_api==false (UI-only entries such as
// gpt-5.3-codex-spark, which reject the Responses API request shape
// regardless of account) — those would fail identically for every
// account, so there's no reason to spend a probe request on them.
// Everything else still needs the per-account probe in ScanWithToken:
// this generic catalog metadata is the same for every caller and
// does not by itself confirm *this* account's plan/trust level can
// reach a given slug.
func FetchModels(ctx context.Context, accessToken string, opts ScanOptions) ([]FetchedModel, error) {
	opts.defaults()

	url := opts.ModelsEndpoint + "?client_version=" + modelsClientVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("originator", opts.Originator)

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-auth: fetch model catalog: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("openai-auth: read model catalog response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai-auth: model catalog HTTP %d: %s", resp.StatusCode, extractDetail(raw, "no detail"))
	}

	var parsed modelsCatalogResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("openai-auth: decode model catalog: %w", err)
	}

	sort.SliceStable(parsed.Models, func(i, j int) bool {
		return parsed.Models[i].Priority < parsed.Models[j].Priority
	})

	out := make([]FetchedModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Visibility != "list" || !m.SupportedInAPI {
			continue
		}
		out = append(out, FetchedModel{Slug: m.Slug, DisplayName: m.DisplayName})
	}
	return out, nil
}

// retryDelay is the sleep between transient-error retries. Exposed as
// a package var so scanner_test can set it to 0 and avoid bloating
// test wall time. Production callers always see the real delay.
var retryDelay = 1500 * time.Millisecond

// maxAttempts is the per-candidate attempt cap. 4xx responses are
// final on the first try (the backend is saying "no, this model
// isn't available for this account"); only network errors and 5xx
// retry, up to this many total attempts.
const maxAttempts = 3

// ScanResult captures one candidate's outcome after the probe's
// retry loop. Status is "200" / "OK" on success, the HTTP status code
// as a string for hard-rejected candidates, or "ERR" when the request
// never completed (network, marshal, etc.). Detail carries a short
// server message or the network error text — useful for the CLI
// table and for the persisted ModelsFile, which exposes the full
// per-candidate breakdown so users can audit a verdict (e.g. why
// gpt-5.4 ended up OK: did we get a 200 or a 429 the heuristic
// accepted?).
type ScanResult struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

// Scan loads the token at path (refreshing if expired), then fetches
// the live model catalog and probes each candidate. Network/refresh
// failures and catalog-fetch failures abort the scan; per-candidate
// probe failures are absorbed and end up in the corresponding
// ScanResult.
func Scan(ctx context.Context, path string, opts ScanOptions) ([]ScanResult, error) {
	ts, err := Load(path)
	if err != nil {
		return nil, err
	}
	if ts.AccessToken == "" {
		return nil, errors.New("openai-auth: token store has no access_token")
	}
	if ts.IsExpired(30 * time.Second) {
		fresh, err := Refresh(ctx, ts, LoginOptions{})
		if err != nil {
			return nil, fmt.Errorf("refresh expired token: %w", err)
		}
		if err := Save(path, fresh); err != nil {
			return nil, fmt.Errorf("save refreshed token: %w", err)
		}
		ts = fresh
	}
	return ScanWithToken(ctx, ts.AccessToken, opts)
}

// ScanWithToken is the inner form for callers that already hold a
// fresh access token (the three login flows do, having just exchanged
// or refreshed it). Fetches the live candidate list via FetchModels,
// then probes each candidate against ProbeEndpoint to confirm this
// account can actually reach it — the catalog's supported_in_api flag
// says the slug is API-shaped in general, not that this account's
// plan/trust level is entitled to it (the gap that let gpt-5.3-codex
// and gpt-5.2 keep 400ing under the old static candidate list even
// though the account had no access).
func ScanWithToken(ctx context.Context, accessToken string, opts ScanOptions) ([]ScanResult, error) {
	opts.defaults()
	candidates, err := FetchModels(ctx, accessToken, opts)
	if err != nil {
		return nil, err
	}
	results := make([]ScanResult, 0, len(candidates))
	for _, m := range candidates {
		if err := ctx.Err(); err != nil {
			results = append(results, ScanResult{Name: m.Slug, DisplayName: m.DisplayName, OK: false, Status: "ERR", Detail: err.Error()})
			continue
		}
		results = append(results, probeOne(ctx, opts.HTTPClient, opts.ProbeEndpoint, opts.Originator, accessToken, m))
	}
	return results, nil
}

// OKNames returns the names of results where OK==true, preserving
// candidate order. Convenience for the persist call site.
func OKNames(results []ScanResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.OK {
			out = append(out, r.Name)
		}
	}
	return out
}

// OKDisplayNames returns the display-name map for OK results,
// keyed by slug. Convenience for the persist call site — pairs with
// OKNames to build a ModelsFile.
func OKDisplayNames(results []ScanResult) map[string]string {
	out := make(map[string]string, len(results))
	for _, r := range results {
		if r.OK && r.DisplayName != "" {
			out[r.Name] = r.DisplayName
		}
	}
	return out
}

// ScanAndPersist runs the scan with default options and on success
// writes the models file at DefaultModelsPath. On any failure
// (network, refresh, ErrNoModelsAvailable) the models file is left
// untouched — callers surface the error to the user.
//
// This is the helper called by the wizard, TUI inline, and CLI login
// flows; centralising it keeps the post-login UX consistent.
func ScanAndPersist(ctx context.Context, accessToken string) (models []string, err error) {
	return ScanAndPersistWithOptions(ctx, accessToken, ScanOptions{})
}

// ScanAndPersistWithOptions is the configurable form of ScanAndPersist.
// Tests inject an httptest endpoint here; production callers use the
// thin wrapper above.
func ScanAndPersistWithOptions(ctx context.Context, accessToken string, opts ScanOptions) (models []string, err error) {
	results, err := ScanWithToken(ctx, accessToken, opts)
	if err != nil {
		return nil, err
	}
	ok := OKNames(results)
	if len(ok) == 0 {
		return nil, ErrNoModelsAvailable
	}
	path, err := DefaultModelsPath()
	if err != nil {
		return nil, fmt.Errorf("resolve models path: %w", err)
	}
	mf := ModelsFile{
		ScannedAt:    time.Now().UTC(),
		Models:       ok,
		DisplayNames: OKDisplayNames(results),
	}
	if err := SaveModels(path, mf); err != nil {
		return nil, fmt.Errorf("save models file: %w", err)
	}
	return ok, nil
}

// probeOne handles the per-candidate retry loop. Network errors and
// 5xx responses retry up to maxAttempts; 4xx responses are final on
// the first attempt (the backend has answered "no" — retrying won't
// change that). The returned ScanResult reflects the last attempt.
func probeOne(ctx context.Context, client *http.Client, endpoint, originator, token string, model FetchedModel) ScanResult {
	var last ScanResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = probeAttempt(ctx, client, endpoint, originator, token, model)
		if last.OK {
			return last
		}
		if !isTransient(last) {
			return last
		}
		if attempt == maxAttempts {
			return last
		}
		select {
		case <-ctx.Done():
			return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: false, Status: "ERR", Detail: ctx.Err().Error()}
		case <-time.After(retryDelay):
		}
	}
	return last
}

// isTransient decides whether a failed ScanResult should retry.
// Network errors (Status="ERR") and 5xx HTTP responses retry; 4xx
// responses are final ("no, this model isn't available for this
// account").
func isTransient(r ScanResult) bool {
	if r.Status == "ERR" {
		return true
	}
	// Status is the HTTP code as a string in the non-ERR path.
	if len(r.Status) == 3 && r.Status[0] == '5' {
		return true
	}
	return false
}

// probeAttempt is one HTTP round-trip against the codex responses
// endpoint. The per-candidate retry loop in probeOne wraps this.
func probeAttempt(ctx context.Context, client *http.Client, endpoint, originator, token string, model FetchedModel) ScanResult {
	body := map[string]any{
		"model":        model.Slug,
		"instructions": "You are a helpful assistant. Reply briefly.",
		"input": []map[string]string{
			{"role": "user", "content": "say hi"},
		},
		// stream:true and store:false are both mandatory on the
		// ChatGPT-account backend (`Stream must be set to true`,
		// `Store must be set to false`). We don't actually consume
		// the SSE here — a 200 with text/event-stream is enough to
		// confirm the model is in the allow-list.
		"stream": true,
		"store":  false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: false, Status: "ERR", Detail: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: false, Status: "ERR", Detail: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", originator)

	resp, err := client.Do(req)
	if err != nil {
		return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: false, Status: "ERR", Detail: err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusOK {
		return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: true, Status: "OK", Detail: "200, response received"}
	}
	// 429 from the codex backend on a one-shot probe is "this model is
	// in your plan but you've consumed the per-model quota for now." The
	// model IS available — the user can pick it and use it once their
	// quota resets. Treating 429 as unavailable would silently hide
	// usable models on free / lightly-rate-limited accounts.
	if resp.StatusCode == http.StatusTooManyRequests {
		detail := extractDetail(respBody, "429, usage limited")
		return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: true, Status: "429", Detail: detail}
	}
	return ScanResult{Name: model.Slug, DisplayName: model.DisplayName, OK: false, Status: fmt.Sprintf("%d", resp.StatusCode), Detail: extractDetail(respBody, "")}
}

// extractDetail pulls a human-friendly explanation out of an OpenAI
// error body. Tries `detail` first (the codex backend's shape), then
// `error.message` (the standard OpenAI shape). Falls back to fallback
// when neither field is present.
func extractDetail(raw []byte, fallback string) string {
	var parsed struct {
		Detail string `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		msg := parsed.Detail
		if msg == "" {
			msg = parsed.Error.Message
		}
		if msg != "" {
			return strings.TrimSpace(msg)
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return fallback
}
