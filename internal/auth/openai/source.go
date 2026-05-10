package openai

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// TokenSource is a thread-safe accessor for the persisted OAuth
// token. It lazy-loads on first call, refreshes on expiry, and
// single-flights concurrent refreshes via a mutex so multiple
// goroutines making simultaneous adapter requests don't all try
// to refresh at once.
type TokenSource struct {
	path       string
	httpClient *http.Client
	issuer     string
	clientID   string
	leeway     time.Duration

	mu     sync.Mutex
	cache  TokenSet
	loaded bool
}

// NewTokenSource returns a TokenSource backed by the file at path.
// Defaults to the production issuer + Codex client_id; tests
// override via setters.
func NewTokenSource(path string) *TokenSource {
	return &TokenSource{
		path:     path,
		issuer:   DefaultIssuerURL,
		clientID: DefaultClientID,
		leeway:   30 * time.Second,
	}
}

// SetHTTPClient overrides the http.Client used for refresh requests.
// Tests inject httptest-backed clients here; production callers
// leave it unset (refresh path uses http.DefaultClient).
func (s *TokenSource) SetHTTPClient(c *http.Client) { s.httpClient = c }

// SetIssuer / SetClientID let tests point the refresh path at an
// httptest server with arbitrary fixtures.
func (s *TokenSource) SetIssuer(url string)  { s.issuer = url }
func (s *TokenSource) SetClientID(id string) { s.clientID = id }

// Token returns a valid (non-expired) access token. If the cached
// token is past its expiry minus the leeway window, Token refreshes
// it (saving the new TokenSet to disk) before returning. ErrNotFound
// surfaces verbatim when no token file exists at the configured
// path — the caller's job to translate that into the user-facing
// "log in" hint (see openAIAuthLoginHint in internal/adapter).
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return "", err
	}
	if !s.cache.IsExpired(s.leeway) {
		return s.cache.AccessToken, nil
	}
	return s.refreshLocked(ctx)
}

// ForceRefresh bypasses the expiry check and refreshes
// unconditionally. Adapter callers use this on a 401 — the access
// token's clock claimed it was valid but the server disagreed, so
// fall back to the refresh path before giving up.
func (s *TokenSource) ForceRefresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	_, err := s.refreshLocked(ctx)
	return err
}

// Current returns the cached TokenSet without doing any I/O. Used
// for status / diagnostic surfaces (e.g. `auth status`) where we
// want to display claims even on an expired token.
func (s *TokenSource) Current() (TokenSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return TokenSet{}, err
	}
	return s.cache, nil
}

func (s *TokenSource) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	ts, err := Load(s.path)
	if err != nil {
		return err
	}
	s.cache = ts
	s.loaded = true
	return nil
}

func (s *TokenSource) refreshLocked(ctx context.Context) (string, error) {
	if s.cache.RefreshToken == "" {
		return "", ErrNoRefreshToken
	}
	fresh, err := RefreshToken(ctx, s.httpClient, s.issuer, s.clientID, s.cache.RefreshToken)
	if err != nil {
		return "", err
	}
	// RefreshToken decodes claims off the new access_token, so
	// AccountID/Email come from there. Server may omit either if
	// the access token is identity-light — keep the cached values
	// as a fallback so `auth status` doesn't suddenly drop the email.
	if fresh.AccountID == "" {
		fresh.AccountID = s.cache.AccountID
	}
	if fresh.Email == "" {
		fresh.Email = s.cache.Email
	}
	if err := Save(s.path, fresh); err != nil {
		return "", fmt.Errorf("openai-auth: persist refreshed token: %w", err)
	}
	s.cache = fresh
	return fresh.AccessToken, nil
}
