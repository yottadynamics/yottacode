package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Live queries a provider's list-models endpoint at runtime. Used
// for non-curated providers — Ollama (lists locally-installed models,
// genuinely runtime state) and openai-compatible endpoints (xAI,
// NVIDIA NIM, custom proxies — too varied to script-curate).
//
// The response is mapped onto our common Model schema, but most
// fields stay zero/nil because these list endpoints return only the
// model id. The picker shows "—" for missing fields.
//
// Errors surface so callers can render "couldn't reach API"; they
// should not silently fall back, since for Ollama a failed probe
// usually means the daemon isn't running and the user needs to know.
func Live(ctx context.Context, kind, baseURL, apiKey string) ([]Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}
	switch kind {
	case "openai", "openai-compatible":
		return liveOpenAICompatible(ctx, kind, baseURL, apiKey)
	case "ollama":
		return liveOllama(ctx, baseURL)
	default:
		return nil, fmt.Errorf("catalog: live fetch unsupported for kind %q", kind)
	}
}

// liveOpenAICompatible covers OpenAI itself (when the user has it
// configured as kind=openai-compatible against the OpenAI URL) and
// the long tail of OpenAI-shape endpoints — xAI, Groq, OpenRouter,
// NVIDIA NIM, Together, vLLM. Response is {data: [{id, ...}]}; we
// keep only the id since the rest is non-uniform across providers.
func liveOpenAICompatible(ctx context.Context, kind, baseURL, apiKey string) ([]Model, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("openai-compatible: empty base URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	body, err := doLive(req)
	if err != nil {
		return nil, err
	}
	var env struct {
		Data []struct {
			ID string `json:"id"`
			// Context-window field names vary across OpenAI-compatible
			// servers; we read whichever the provider volunteers. vLLM
			// emits max_model_len, OpenRouter/Together emit
			// context_length, some proxies emit context_window. OpenAI
			// itself and DeepSeek's official API report none — those
			// rows keep ContextWindow zero and fall back to the
			// default_window / model-tag table downstream.
			ContextLength int `json:"context_length"`
			MaxModelLen   int `json:"max_model_len"`
			ContextWindow int `json:"context_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%s: parse response: %w", kind, err)
	}
	out := make([]Model, 0, len(env.Data))
	for _, m := range env.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, Model{
			ID:            id,
			Provider:      kind,
			ContextWindow: firstPositive(m.ContextLength, m.MaxModelLen, m.ContextWindow),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// liveOllama queries Ollama's /api/tags endpoint for locally-installed
// models. The base URL on Ollama profiles typically ends in /v1 (the
// OpenAI-compatible shim path), but /api/tags lives at the root, so
// we strip a trailing /v1 before composing the URL. No auth.
func liveOllama(ctx context.Context, baseURL string) ([]Model, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("ollama: empty base URL")
	}
	base := strings.TrimRight(baseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	body, err := doLive(req)
	if err != nil {
		return nil, err
	}
	var env struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("ollama: parse response: %w", err)
	}
	out := make([]Model, 0, len(env.Models))
	for _, m := range env.Models {
		id := strings.TrimSpace(m.Name)
		if id == "" {
			continue
		}
		out = append(out, Model{ID: id, Provider: "ollama"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ollamaContextWindow resolves a single Ollama model's effective context
// window from /api/show. Ollama's /api/tags (used by liveOllama) carries
// no window and Ollama doesn't reject on context overflow, so the
// openai-compatible overflow probe can't help here — but /api/show does
// report the model's context length, so we read it directly for the one
// model. Returns 0 when /api/show is unreachable or reports neither
// source.
//
// Resolution prefers a num_ctx pinned in the model's Modelfile (the
// "parameters" field) — that's the window Ollama actually serves at
// runtime — and falls back to the architectural context_length the model
// was trained with (model_info).
//
// Caveat: when num_ctx isn't pinned, Ollama serves at its own server
// default (commonly 4096), which can be SMALLER than the architectural
// ceiling returned here. The ceiling is still far better than the blind
// default_window, but for an exact figure pin `PARAMETER num_ctx` in the
// Modelfile or set context_window in config.
//
// baseURL is the Ollama profile's URL; it usually ends in /v1 (the
// OpenAI-compatible shim), but /api/show lives at the root, so a trailing
// /v1 is stripped — mirroring liveOllama.
func ollamaContextWindow(ctx context.Context, baseURL, model string) int {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	// Send both "model" (current) and "name" (deprecated alias) so the
	// request works across Ollama versions; the server reads whichever it
	// knows and ignores the other.
	payload, err := json.Marshal(map[string]string{"model": model, "name": model})
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/show", bytes.NewReader(payload))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := doLive(req)
	if err != nil {
		return 0
	}
	var env struct {
		Parameters string         `json:"parameters"`
		ModelInfo  map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0
	}
	if n := parseOllamaNumCtx(env.Parameters); n > 0 {
		return n
	}
	return ollamaArchContextLength(env.ModelInfo)
}

// parseOllamaNumCtx pulls a `num_ctx <N>` value out of /api/show's
// newline-separated "parameters" blob (the model's Modelfile PARAMETER
// lines). Returns 0 when num_ctx isn't pinned.
func parseOllamaNumCtx(params string) int {
	for _, line := range strings.Split(params, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "num_ctx" {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// ollamaArchContextLength reads the architectural context length out of
// /api/show's model_info map: the value of "<arch>.context_length" where
// arch is general.architecture (e.g. "llama.context_length"), falling
// back to any "*.context_length" key. Returns 0 when none is present.
func ollamaArchContextLength(info map[string]any) int {
	if info == nil {
		return 0
	}
	if arch, ok := info["general.architecture"].(string); ok && arch != "" {
		if v := asPositiveInt(info[arch+".context_length"]); v > 0 {
			return v
		}
	}
	for k, val := range info {
		if strings.HasSuffix(k, ".context_length") {
			if v := asPositiveInt(val); v > 0 {
				return v
			}
		}
	}
	return 0
}

// asPositiveInt coerces a JSON-decoded number (float64 from a map[string]any
// decode, but int / json.Number are handled defensively) to a positive int,
// or 0.
func asPositiveInt(v any) int {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return int(i)
		}
	}
	return 0
}

// firstPositive returns the first argument greater than zero, or 0 when
// none qualify. Used to pick a context-window value from whichever
// field a given OpenAI-compatible provider populated.
func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func doLive(req *http.Request) ([]byte, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
