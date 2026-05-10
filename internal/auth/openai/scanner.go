package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultCodexEndpoint is the ChatGPT-account Responses-API URL the
// scanner probes against. Distinct from api.openai.com/v1/responses —
// see the comment on the adapter's OpenAIAuthEndpoint for the
// per-request contract differences.
const DefaultCodexEndpoint = "https://chatgpt.com/backend-api/codex/responses"

// DefaultCandidates is the maintainer-curated list of model names the
// scanner probes after each successful login. Refresh by editing this
// slice when OpenAI broadens or narrows the ChatGPT-account allow-list,
// then rebuild — `yottacode openai-auth login --candidates ...` lets
// you test new names ad-hoc without rebuilding.
var DefaultCandidates = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.2",
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

// ErrNoModelsAvailable is returned by Scan / ScanAndPersist when the
// scan ran end-to-end but every candidate was rejected. Distinct from
// network errors so callers can render a more specific message
// ("your account may not have access to any of the candidates").
var ErrNoModelsAvailable = errors.New("openai-auth: scan returned zero usable models")

// ScanResult captures one candidate's outcome after the retry loop.
// Status is "200" / "OK" on success, the HTTP status code as a string
// for hard-rejected candidates, or "ERR" when the request never
// completed (network, marshal, etc.). Detail carries a short server
// message or the network error text — useful for the CLI table and
// for the persisted ModelsFile.Results field, which exposes the
// full per-candidate breakdown so users can audit a verdict
// (e.g. why gpt-5.4 ended up in the OK set: did we get a 200 or a
// 429 the heuristic accepted?).
type ScanResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ScanOptions tunes Scan / ScanWithToken. Zero values pick the
// production defaults so callers can `Scan(ctx, path, ScanOptions{})`
// with no boilerplate.
type ScanOptions struct {
	HTTPClient *http.Client
	Endpoint   string
	Originator string
	Candidates []string
}

func (o *ScanOptions) defaults() {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultCodexEndpoint
	}
	if o.Originator == "" {
		o.Originator = DefaultOriginator
	}
	if len(o.Candidates) == 0 {
		o.Candidates = DefaultCandidates
	}
}

// Scan loads the token at path (refreshing if expired), then probes
// each candidate against the codex backend. Returns one ScanResult
// per candidate in input order. Network/refresh failures abort the
// scan — per-candidate transient failures are absorbed by the retry
// loop and end up in the corresponding ScanResult.
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
	return ScanWithToken(ctx, ts.AccessToken, opts), nil
}

// ScanWithToken is the inner form for callers that already hold a
// fresh access token (the three login flows do, having just exchanged
// or refreshed it). Skips the Load+Refresh dance.
func ScanWithToken(ctx context.Context, accessToken string, opts ScanOptions) []ScanResult {
	opts.defaults()
	results := make([]ScanResult, 0, len(opts.Candidates))
	for _, name := range opts.Candidates {
		if err := ctx.Err(); err != nil {
			results = append(results, ScanResult{Name: name, OK: false, Status: "ERR", Detail: err.Error()})
			continue
		}
		results = append(results, probeOne(ctx, opts.HTTPClient, opts.Endpoint, opts.Originator, accessToken, name))
	}
	return results
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
	opts.defaults()
	results := ScanWithToken(ctx, accessToken, opts)
	ok := OKNames(results)
	if len(ok) == 0 {
		return nil, ErrNoModelsAvailable
	}
	path, err := DefaultModelsPath()
	if err != nil {
		return nil, fmt.Errorf("resolve models path: %w", err)
	}
	mf := ModelsFile{
		ScannedAt:  time.Now().UTC(),
		Candidates: append([]string(nil), opts.Candidates...),
		Models:     ok,
		Results:    append([]ScanResult(nil), results...),
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
func probeOne(ctx context.Context, client *http.Client, endpoint, originator, token, model string) ScanResult {
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
			return ScanResult{Name: model, OK: false, Status: "ERR", Detail: ctx.Err().Error()}
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

// probeAttempt is one HTTP round-trip against the codex endpoint.
// The per-candidate retry loop in probeOne wraps this.
func probeAttempt(ctx context.Context, client *http.Client, endpoint, originator, token, model string) ScanResult {
	body := map[string]any{
		"model":        model,
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
		return ScanResult{Name: model, OK: false, Status: "ERR", Detail: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return ScanResult{Name: model, OK: false, Status: "ERR", Detail: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", originator)

	resp, err := client.Do(req)
	if err != nil {
		return ScanResult{Name: model, OK: false, Status: "ERR", Detail: err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusOK {
		return ScanResult{Name: model, OK: true, Status: "OK", Detail: "200, response received"}
	}
	// 429 from the codex backend on a one-shot probe is "this model is
	// in your plan but you've consumed the per-model quota for now." The
	// model IS available — the user can pick it and use it once their
	// quota resets. Treating 429 as unavailable would silently hide
	// usable models on free / lightly-rate-limited accounts.
	if resp.StatusCode == http.StatusTooManyRequests {
		detail := extractDetail(respBody, "429, usage limited")
		return ScanResult{Name: model, OK: true, Status: "429", Detail: detail}
	}
	return ScanResult{Name: model, OK: false, Status: fmt.Sprintf("%d", resp.StatusCode), Detail: extractDetail(respBody, "")}
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
