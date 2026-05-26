package copilot

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// TokenSource is a thread-safe accessor for the Copilot API token. It
// lazy-loads the GitHub OAuth token from disk, exchanges it for a
// short-lived Copilot token, and caches/refreshes the Copilot token
// as needed. The GitHub token is long-lived; the Copilot token
// typically expires in ~30 minutes.
type TokenSource struct {
	path           string
	httpClient     *http.Client
	tokenEndpoint  string
	leeway         time.Duration

	mu          sync.Mutex
	github      TokenSet
	copilot     CopilotToken
	githubLoaded bool
}

// NewTokenSource returns a TokenSource backed by the file at path.
func NewTokenSource(path string) *TokenSource {
	return &TokenSource{
		path:          path,
		tokenEndpoint: CopilotTokenEndpoint,
		leeway:        60 * time.Second,
	}
}

// SetHTTPClient overrides the http.Client used for token exchange.
func (s *TokenSource) SetHTTPClient(c *http.Client) { s.httpClient = c }

// SetTokenEndpoint overrides the Copilot token endpoint (for tests).
func (s *TokenSource) SetTokenEndpoint(url string) { s.tokenEndpoint = url }

// Token returns a valid Copilot API bearer token. If the cached
// Copilot token is expired, it exchanges the GitHub token for a fresh
// one. ErrNotFound surfaces when no token file exists at the
// configured path.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureGitHubLoadedLocked(); err != nil {
		return "", err
	}
	if s.copilot.Token != "" && time.Now().Add(s.leeway).Before(s.copilot.ExpiresAtTime()) {
		return s.copilot.Token, nil
	}
	return s.refreshCopilotLocked(ctx)
}

// APIEndpoint returns the Copilot API base URL from the last token
// exchange. Defaults to CopilotAPIEndpoint if no exchange has
// happened yet.
func (s *TokenSource) APIEndpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.copilot.Endpoints.API != "" {
		return s.copilot.Endpoints.API
	}
	return CopilotAPIEndpoint
}

// ForceRefresh unconditionally re-fetches the Copilot token. Used on
// 401 responses from the API.
func (s *TokenSource) ForceRefresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureGitHubLoadedLocked(); err != nil {
		return err
	}
	_, err := s.refreshCopilotLocked(ctx)
	return err
}

// GitHubToken returns the stored GitHub OAuth token for status display.
func (s *TokenSource) GitHubToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureGitHubLoadedLocked(); err != nil {
		return "", err
	}
	return s.github.GitHubToken, nil
}

func (s *TokenSource) ensureGitHubLoadedLocked() error {
	if s.githubLoaded {
		return nil
	}
	ts, err := Load(s.path)
	if err != nil {
		return err
	}
	s.github = ts
	s.githubLoaded = true
	return nil
}

func (s *TokenSource) refreshCopilotLocked(ctx context.Context) (string, error) {
	ct, err := FetchCopilotTokenFrom(ctx, s.httpClient, s.github.GitHubToken, s.tokenEndpoint)
	if err != nil {
		return "", fmt.Errorf("copilot: refresh API token: %w", err)
	}
	s.copilot = ct
	return ct.Token, nil
}
