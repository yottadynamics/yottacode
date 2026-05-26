package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	CopilotTokenEndpoint = "https://api.github.com/copilot_internal/v2/token"
	CopilotAPIEndpoint   = "https://api.githubcopilot.com"
)

// CopilotToken is the short-lived API token returned by the Copilot
// token endpoint. Typically expires in ~30 minutes.
type CopilotToken struct {
	Token     string   `json:"token"`
	ExpiresAt int64    `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// ExpiresAtTime returns ExpiresAt as a time.Time.
func (ct CopilotToken) ExpiresAtTime() time.Time {
	return time.Unix(ct.ExpiresAt, 0)
}

// FetchCopilotToken exchanges a GitHub OAuth token for a short-lived
// Copilot API token.
func FetchCopilotToken(ctx context.Context, httpClient *http.Client, githubToken string) (CopilotToken, error) {
	return FetchCopilotTokenFrom(ctx, httpClient, githubToken, CopilotTokenEndpoint)
}

// FetchCopilotTokenFrom is the test-friendly form that accepts a
// custom endpoint URL.
func FetchCopilotTokenFrom(ctx context.Context, httpClient *http.Client, githubToken, endpoint string) (CopilotToken, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CopilotToken{}, err
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.250.0")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return CopilotToken{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return CopilotToken{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return CopilotToken{}, fmt.Errorf("copilot: GitHub token rejected (401) — re-run `yottacode copilot-auth login`")
	}
	if resp.StatusCode == http.StatusForbidden {
		detail := strings.TrimSpace(string(raw))
		if detail == "" {
			detail = "no Copilot subscription or OAuth app not authorized"
		}
		return CopilotToken{}, fmt.Errorf("copilot: 403 Forbidden — %s", detail)
	}
	if resp.StatusCode != http.StatusOK {
		return CopilotToken{}, fmt.Errorf("copilot: token exchange failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var ct CopilotToken
	if err := json.Unmarshal(raw, &ct); err != nil {
		return CopilotToken{}, fmt.Errorf("copilot: decode token response: %w", err)
	}
	if ct.Token == "" {
		return CopilotToken{}, fmt.Errorf("copilot: empty token in response")
	}
	if ct.Endpoints.API == "" {
		ct.Endpoints.API = CopilotAPIEndpoint
	}
	return ct, nil
}
