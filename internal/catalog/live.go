package catalog

import (
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
		out = append(out, Model{ID: id, Provider: kind})
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
