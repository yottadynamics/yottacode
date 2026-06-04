package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchURLTool retrieves a single URL over HTTP(S) and returns capped textual
// content. This is the local-network fallback for models that do not have
// provider-native web search.
type FetchURLTool struct {
	// Client overrides the HTTP client. nil (the production default) uses
	// the SSRF-guarded client; tests inject a permissive client to reach a
	// loopback httptest server.
	Client *http.Client
}

func (t *FetchURLTool) httpClient() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return ssrfSafeHTTPClient
}

func (t *FetchURLTool) Name() string { return "fetch_url" }

func (t *FetchURLTool) Description() string {
	return "Fetch a single HTTP or HTTPS URL and return capped textual content. Useful for specific pages or feeds when provider-native web search is unavailable."
}

func (t *FetchURLTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute HTTP or HTTPS URL to fetch.",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Maximum number of response bytes to return. Default 65536.",
			},
		},
		"required": []string{"url"},
	}
}

func (t *FetchURLTool) RequiresApproval(string) bool { return false }
func (t *FetchURLTool) ParallelSafe(string) bool     { return true }

func (t *FetchURLTool) PreviewCall(argsJSON string) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.URL) == "" {
		return "fetch_url(?)"
	}
	return fmt.Sprintf("fetch_url(%s)", a.URL)
}

func (t *FetchURLTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		URL      string `json:"url"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("fetch_url: invalid args: %w", err)
	}
	if strings.TrimSpace(a.URL) == "" {
		return "", fmt.Errorf("fetch_url: url is required")
	}
	if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
		return "", fmt.Errorf("fetch_url: url must start with http:// or https://")
	}
	if a.MaxBytes <= 0 || a.MaxBytes > 256*1024 {
		a.MaxBytes = 64 * 1024
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch_url: %w", err)
	}
	req.Header.Set("User-Agent", "yottacode/1.0")

	// fetch_url auto-executes (no approval) on a model-supplied URL, so it
	// goes through the SSRF-guarded client: the dialer refuses loopback,
	// link-local/metadata, and private addresses at connect time (after DNS
	// resolution and across redirects).
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch_url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch_url: HTTP %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !isTextualContentType(contentType) {
		return "", fmt.Errorf("fetch_url: unsupported content type %q", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(a.MaxBytes+1)))
	if err != nil {
		return "", fmt.Errorf("fetch_url: %w", err)
	}
	truncated := len(body) > a.MaxBytes
	if truncated {
		body = body[:a.MaxBytes]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", a.URL)
	fmt.Fprintf(&b, "Status: %d\n", resp.StatusCode)
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", contentType)
	}
	if truncated {
		fmt.Fprintf(&b, "Note: response truncated to %d bytes\n", a.MaxBytes)
	}
	b.WriteString("\n")
	b.Write(body)
	return b.String(), nil
}

func isTextualContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	switch {
	case strings.HasPrefix(contentType, "text/"),
		strings.Contains(contentType, "json"),
		strings.Contains(contentType, "xml"),
		strings.Contains(contentType, "javascript"),
		strings.Contains(contentType, "html"),
		strings.Contains(contentType, "x-www-form-urlencoded"):
		return true
	default:
		return false
	}
}
