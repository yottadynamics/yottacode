package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Access is the three-state verdict a probe produces. Only AccessDenied
// greys a model out in the picker; AccessUnknown — a transport failure, a
// cancellation, a 5xx, or any status that isn't a per-model verdict — is
// indistinguishable from "never scanned" and must never disable a model
// that may well be callable.
type Access string

const (
	AccessAvailable Access = "available"
	AccessDenied    Access = "denied"
	AccessUnknown   Access = "unknown"
)

// ScanResult captures one candidate model's access probe outcome.
type ScanResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Access Access `json:"access"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// scanResult builds a ScanResult, keeping the legacy OK bool in sync with
// the access verdict so existing readers (the CLI mark, OKNames) keep
// working.
func scanResult(model string, access Access, status, detail string) ScanResult {
	return ScanResult{Name: model, OK: access == AccessAvailable, Access: access, Status: status, Detail: detail}
}

// EffectiveAccess returns the stored verdict, reconstructing it from the
// legacy ok/status fields when reading a pre-Access scan file so old
// caches degrade to the corrected (fail-open) semantics instead of the
// old "any non-OK means denied" conflation.
func (r ScanResult) EffectiveAccess() Access {
	if r.Access != "" {
		return r.Access
	}
	if r.OK {
		return AccessAvailable
	}
	switch r.Status {
	case "403", "404":
		return AccessDenied
	default:
		return AccessUnknown
	}
}

// ScanOptions tunes Scan. Zero values use production defaults.
type ScanOptions struct {
	HTTPClient  *http.Client
	Provider    string
	BaseURL     string
	Candidates  []string
	TokenSource ScannerTokenSource
}

func (o *ScanOptions) defaults() {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.TokenSource == nil {
		o.TokenSource = NewTokenSource()
	}
}

// Scan probes each candidate model against one configured Vertex
// provider. It sends tiny non-streaming requests and records per-model
// access. A 200 proves the project/location can call the model; a 429 is
// also treated as accessible because quota/rate limiting means Vertex
// recognized the model for the project.
//
// A 403 or 404 is a definitive denial (no permission / not published in
// this project+location); every other outcome — a transport error, a
// cancellation, a 400/401, or a 5xx — is recorded as unknown rather than
// denied, so an inconclusive probe never greys out a model that may be
// callable.
func Scan(ctx context.Context, opts ScanOptions) ([]ScanResult, error) {
	opts.defaults()
	if strings.TrimSpace(opts.Provider) == "" {
		return nil, fmt.Errorf("vertex: provider kind is required")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("vertex: base_url is required")
	}
	tok, err := opts.TokenSource.Token(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]ScanResult, 0, len(opts.Candidates))
	for _, model := range opts.Candidates {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			// Cancelled or timed out before we could probe this model —
			// its access is unknown, not denied.
			results = append(results, scanResult(model, AccessUnknown, "ERR", err.Error()))
			continue
		}
		results = append(results, probeModel(ctx, opts.HTTPClient, opts.Provider, opts.BaseURL, tok, model))
	}
	return results, nil
}

// ScanAndPersist runs Scan and writes the model-access cache for this
// provider/baseURL. It persists denials too (not just the reachable set)
// so the picker can grey out models Vertex explicitly refused; models
// whose probe was inconclusive are recorded as unknown and left enabled.
func ScanAndPersist(ctx context.Context, opts ScanOptions) (ModelsFile, string, error) {
	results, err := Scan(ctx, opts)
	if err != nil {
		return ModelsFile{}, "", err
	}
	mf, path, err := PersistScan(opts.Provider, opts.BaseURL, opts.Candidates, results)
	if err != nil {
		return ModelsFile{}, path, fmt.Errorf("vertex: save model access scan: %w", err)
	}
	return mf, path, nil
}

func probeModel(ctx context.Context, client *http.Client, provider, baseURL, token, model string) ScanResult {
	endpoint, body, err := probeRequest(provider, baseURL, model)
	if err != nil {
		return scanResult(model, AccessUnknown, "ERR", err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return scanResult(model, AccessUnknown, "ERR", err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Network failure, timeout, or cancellation — we learned nothing
		// about this model's access, so leave it unknown.
		return scanResult(model, AccessUnknown, "ERR", err.Error())
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	status := fmt.Sprintf("%d", resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusOK:
		return scanResult(model, AccessAvailable, status, "accessible")
	case http.StatusTooManyRequests:
		// Quota/rate limiting proves Vertex recognized the model for this
		// project, which is the access signal we're after.
		return scanResult(model, AccessAvailable, status, extractDetail(respBody, "rate limited; model recognized"))
	case http.StatusForbidden, http.StatusNotFound:
		// The only definitive per-model denials: 403 (no permission) and
		// 404 (model not published / not enabled in this project+location).
		return scanResult(model, AccessDenied, status, extractDetail(respBody, resp.Status))
	default:
		// 400 (our probe payload), 401 (token, not model), 5xx (server) —
		// none is a per-model access verdict, so don't grey the model out.
		return scanResult(model, AccessUnknown, status, extractDetail(respBody, resp.Status))
	}
}

func probeRequest(provider, baseURL, model string) (string, []byte, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch provider {
	case ProviderVertexAnthropic:
		body := map[string]any{
			"anthropic_version": "vertex-2023-10-16",
			"max_tokens":        1,
			"messages": []map[string]string{
				{"role": "user", "content": "Reply with ok."},
			},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s/publishers/anthropic/models/%s:rawPredict", base, model), raw, nil
	case ProviderVertex:
		body := map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": "Reply with ok."},
			},
			"max_tokens": 1,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return "", nil, err
		}
		return base + "/chat/completions", raw, nil
	default:
		return "", nil, fmt.Errorf("vertex: unsupported provider kind %q", provider)
	}
}

func extractDetail(raw []byte, fallback string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		parts := []string{}
		if parsed.Error.Status != "" {
			parts = append(parts, parsed.Error.Status)
		}
		if parsed.Error.Message != "" {
			parts = append(parts, parsed.Error.Message)
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, ": "))
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return fallback
}
