package wizard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// EnvSnapshot is what the wizard learns from the live OS environment
// before any prompt: which provider keys are already exported, and a
// suggestion list so the multi-select can pre-check those rows.
type EnvSnapshot struct {
	// Present maps env-var name → true for every key in os.Environ
	// that matches a known catalog entry's APIKeyEnv. Used to skip
	// the key-prompt step and to render "key present in env" badges.
	Present map[string]bool

	// SuggestedProviders is the set of catalog entry names whose key
	// is in Present, plus "ollama" if the local probe responded.
	// Order follows Catalog so the UI is deterministic.
	SuggestedProviders []string
}

// SnapshotEnv inspects os.Environ for every catalog entry's APIKeyEnv
// and builds an EnvSnapshot. Pure read, no side effects.
func SnapshotEnv() EnvSnapshot {
	snap := EnvSnapshot{Present: map[string]bool{}}
	for _, e := range Catalog {
		if e.APIKeyEnv == "" {
			continue
		}
		if v, ok := os.LookupEnv(e.APIKeyEnv); ok && strings.TrimSpace(v) != "" {
			snap.Present[e.APIKeyEnv] = true
			snap.SuggestedProviders = append(snap.SuggestedProviders, e.Name)
		}
	}
	return snap
}

// OllamaProbeResult is the output of probing a local Ollama server.
type OllamaProbeResult struct {
	Reachable bool
	BaseURL   string   // the URL probed
	Models    []string // models reported by /api/tags, if any
	Err       error    // last error encountered (for diagnostics)
}

// ProbeOllama hits <base>/api/tags with a short timeout and returns
// the installed model names. The default base URL is
// http://localhost:11434 (or $OLLAMA_HOST). The wizard uses this
// result to (a) auto-suggest the Ollama provider and (b) populate the
// model picker for it.
//
// Failure is silent — Ollama might just not be running. The wizard
// shows the catalog's free-form text input in that case.
func ProbeOllama(ctx context.Context, base string) OllamaProbeResult {
	if base == "" {
		base = "http://localhost:11434"
		if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
			if !strings.HasPrefix(h, "http") {
				h = "http://" + h
			}
			base = strings.TrimSuffix(h, "/")
		}
	}
	res := OllamaProbeResult{BaseURL: base}
	probeCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		res.Err = err
		return res
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Err = fmt.Errorf("http %d", resp.StatusCode)
		return res
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		res.Err = err
		return res
	}
	res.Reachable = true
	res.Models = make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" {
			res.Models = append(res.Models, m.Name)
		}
	}
	return res
}

// ValidationStatus reports whether a provided API key works against
// the provider's models endpoint. The wizard surfaces this inline
// next to the key input as a glyph (✓ / ✗ / ⏳) — informational only;
// validation never blocks the wizard.
type ValidationStatus int

const (
	// ValidationUnknown means we haven't probed yet, or probing was
	// skipped (e.g. --skip-validation, no key, no internet).
	ValidationUnknown ValidationStatus = iota
	ValidationOK
	ValidationFailed
)

// ValidationResult bundles the outcome of one provider+key validation.
type ValidationResult struct {
	Status     ValidationStatus
	HTTPStatus int    // 0 when no response (DNS / timeout)
	Detail     string // short message — "ok", "401 Unauthorized", "timeout", …
}

// ValidateKey runs a 3-second-bounded HEAD/GET against a known
// "list-models" endpoint for the given catalog entry. The response is
// not parsed; we only care whether the key was accepted (2xx/3xx) or
// rejected (401/403). DNS / network / timeout failures land as
// ValidationUnknown — we don't pretend a flaky network is a bad key.
//
// Per-kind endpoint:
//   - anthropic        → GET https://api.anthropic.com/v1/models
//     (header x-api-key + anthropic-version)
//   - openai           → GET https://api.openai.com/v1/models
//   - openai-compatible → GET <base>/models
//   - gemini           → GET https://generativelanguage.googleapis.com/v1/models?key=…
//   - ollama           → never validated (no key)
func ValidateKey(ctx context.Context, e CatalogEntry, key string) ValidationResult {
	if strings.TrimSpace(key) == "" || e.Kind == "ollama" {
		return ValidationResult{Status: ValidationUnknown, Detail: "no key"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	url, headerKey, headerVal, extraHeaders := keyValidationRequest(e, key)
	if url == "" {
		return ValidationResult{Status: ValidationUnknown, Detail: "no validation endpoint"}
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return ValidationResult{Status: ValidationUnknown, Detail: err.Error()}
	}
	if headerKey != "" {
		req.Header.Set(headerKey, headerVal)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ValidationResult{Status: ValidationUnknown, Detail: classifyNetErr(err)}
	}
	defer resp.Body.Close()
	res := ValidationResult{HTTPStatus: resp.StatusCode}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		res.Status = ValidationOK
		res.Detail = "ok"
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		res.Status = ValidationFailed
		res.Detail = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	default:
		// 5xx, 429, etc. — provider is unhappy but the key isn't
		// definitively bad. Don't block; mark unknown.
		res.Status = ValidationUnknown
		res.Detail = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return res
}

// keyValidationRequest returns the URL + auth header for a quick
// list-models probe. Extracted as a separate function so tests can
// drive the URL through httptest.NewServer by overriding e.BaseURL.
func keyValidationRequest(e CatalogEntry, key string) (url, headerKey, headerVal string, extra map[string]string) {
	switch e.Kind {
	case "anthropic":
		base := strings.TrimRight(e.BaseURL, "/")
		return base + "/v1/models", "x-api-key", key, map[string]string{
			"anthropic-version": "2023-06-01",
		}
	case "gemini":
		base := strings.TrimRight(e.BaseURL, "/")
		return base + "/v1/models", "x-goog-api-key", key, nil
	case "openai", "openai-compatible":
		base := strings.TrimRight(e.BaseURL, "/")
		// Many OpenAI-compatible endpoints expect /v1/models; the
		// catalog's BaseURL already includes /v1 for those, so we
		// just append /models. The "openai" kind also has /v1 in its
		// catalog BaseURL.
		return base + "/models", "Authorization", "Bearer " + key, nil
	}
	return "", "", "", nil
}

// classifyNetErr produces a short, user-readable description of a
// network failure. Reading "context deadline exceeded" in the wizard
// is jarring; "timeout" reads cleanly.
func classifyNetErr(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "no such host"):
		return "no DNS"
	case strings.Contains(msg, "connection refused"):
		return "refused"
	}
	return msg
}
