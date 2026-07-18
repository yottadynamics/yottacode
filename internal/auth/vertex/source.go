// Package vertex resolves Google Cloud Application Default Credentials
// into the short-lived OAuth access tokens Vertex AI requests carry.
//
// Unlike every other provider yottacode talks to, Vertex has no API key:
// the credential is an access token that expires in ~1 hour, so it has to
// be minted per request rather than read once from api_key_env. That is
// the whole reason the vertex kinds exist separately from
// openai-compatible — see internal/adapter/vertex.go.
package vertex

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scope is the OAuth scope every Vertex AI call needs. cloud-platform is
// the coarse grant Google documents for aiplatform.googleapis.com; there
// is no narrower scope that covers streamGenerateContent/streamRawPredict.
const Scope = "https://www.googleapis.com/auth/cloud-platform"

// earlyExpiry makes the underlying reuse-source refresh this far ahead of
// real expiry. oauth2's own default is 10s, which is cutting it fine: a
// token that passes the check and then expires before the request is
// admitted fails the whole turn, and a turn is expensive to retry. ADC
// tokens live ~1h, so refreshing 5 minutes early costs nothing.
const earlyExpiry = 5 * time.Minute

// SetupHint is the actionable half of every credential error here. Single
// source of truth so the adapter, /doctor, and the picker all say the same
// thing.
const SetupHint = "run `gcloud auth application-default login`, or point $GOOGLE_APPLICATION_CREDENTIALS at a service-account key"

// TokenSource hands out Vertex access tokens, resolving Application
// Default Credentials on first use.
//
// Credential lookup is lazy, not done at construction: FindDefaultCredentials
// probes the GCE metadata server when no file or env var is present, and
// paying that timeout on every TUI startup — including for users who have
// never touched Vertex — is not worth failing a millisecond earlier.
//
// Safe for concurrent use. The mutex guards the one-time credential load;
// token caching and refresh happen inside the oauth2 reuse-source, which
// is itself concurrency-safe.
type TokenSource struct {
	mu    sync.Mutex
	find  func(context.Context, ...string) (*google.Credentials, error)
	creds *google.Credentials
	ts    oauth2.TokenSource
}

// NewTokenSource returns a TokenSource backed by Application Default
// Credentials.
func NewTokenSource() *TokenSource {
	return &TokenSource{find: google.FindDefaultCredentials}
}

// ProjectID reports the project the resolved credentials belong to, or ""
// when the credentials carry none (user ADC often does not). Callers must
// not depend on it — yottacode takes the project from the provider's
// base_url, not from the credential.
func (s *TokenSource) ProjectID(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		return ""
	}
	return s.creds.ProjectID
}

// Token returns a valid Vertex AI access token, refreshing it when the
// cached one is within earlyExpiry of expiring.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(ctx); err != nil {
		return "", err
	}
	tok, err := s.ts.Token()
	if err != nil {
		return "", fmt.Errorf("vertex: refresh access token: %w — %s", err, SetupHint)
	}
	return tok.AccessToken, nil
}

func (s *TokenSource) ensureLoadedLocked(ctx context.Context) error {
	if s.ts != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("vertex: no Application Default Credentials found — %s: %w", SetupHint, err)
	}
	// google.FindDefaultCredentials wires the lookup context into some
	// returned token sources. Do not pass a short-lived probe/request
	// context through: the lookup may succeed, then the caller cancels that
	// context, and the cached token source fails every later refresh with
	// context.Canceled. The caller's context is checked above so already-
	// canceled calls still fail fast, but the durable source is rooted in a
	// durable context.
	creds, err := s.find(context.Background(), Scope)
	if err != nil {
		return fmt.Errorf("vertex: no Application Default Credentials found — %s: %w", SetupHint, err)
	}
	s.creds = creds
	s.ts = oauth2.ReuseTokenSourceWithExpiry(nil, creds.TokenSource, earlyExpiry)
	return nil
}
