package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// EmbedClient talks to a local Ollama embedding endpoint.
type EmbedClient struct {
	BaseURL string
	Model   string
	Timeout time.Duration
}

// NewEmbedClient returns a client configured for local Ollama embeddings.
// BaseURL defaults to http://localhost:11434 (or $OLLAMA_HOST).
// Model defaults to "nomic-embed-text".
func NewEmbedClient(baseURL, model string) *EmbedClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
		if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
			if !strings.HasPrefix(h, "http") {
				h = "http://" + h
			}
			baseURL = strings.TrimSuffix(h, "/")
		}
	}
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	baseURL = strings.TrimSuffix(baseURL, "/")
	if model == "" {
		model = "nomic-embed-text"
	}
	return &EmbedClient{
		BaseURL: baseURL,
		Model:   model,
		Timeout: 30 * time.Second,
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns the embedding vector for a single text input.
// Calls POST /api/embeddings on the configured Ollama server.
func (c *EmbedClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	body, err := json.Marshal(embedRequest{Model: c.Model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: http %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty embedding returned")
	}
	return result.Embedding, nil
}

// Available reports whether the configured embedding model is
// installed on the Ollama server. Uses a short probe timeout.
func (c *EmbedClient) Available(ctx context.Context) bool {
	_, installed := c.Status(ctx)
	return installed
}

// Status probes the Ollama server and reports separately whether the
// server is reachable and whether the configured model is installed.
// Distinguishes "Ollama isn't running" (reachable=false) from "Ollama
// is running but the model was removed" (reachable=true, installed=false)
// so callers can surface a targeted warning.
func (c *EmbedClient) Status(ctx context.Context) (reachable, installed bool) {
	probeCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return false, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}

	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, false
	}
	reachable = true
	for _, m := range body.Models {
		name := strings.TrimSpace(m.Name)
		if name == c.Model || strings.HasPrefix(name, c.Model+":") {
			installed = true
			return
		}
	}
	return
}
