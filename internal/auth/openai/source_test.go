package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIssuer mounts a /oauth/token handler that mints fresh access
// tokens on demand and counts calls. Tests use it to verify single-
// flight behavior + cache reuse.
type fakeIssuer struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-atk","refresh_token":"fresh-rtk","token_type":"bearer","expires_in":3600}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeStore(t *testing.T, dir string, ts TokenSet) string {
	t.Helper()
	path := filepath.Join(dir, "openai-auth.json")
	if err := Save(path, ts); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTokenSourceCachedRead(t *testing.T) {
	dir := t.TempDir()
	path := writeStore(t, dir, TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	src := NewTokenSource(path)
	for i := 0; i < 3; i++ {
		got, err := src.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != "atk" {
			t.Errorf("got %q, want atk", got)
		}
	}
}

func TestTokenSourceMissingFile(t *testing.T) {
	src := NewTokenSource(filepath.Join(t.TempDir(), "nope.json"))
	_, err := src.Token(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestTokenSourceRefreshOnExpiry(t *testing.T) {
	iss := newFakeIssuer(t)
	dir := t.TempDir()
	// expired ten seconds ago
	path := writeStore(t, dir, TokenSet{
		AccessToken:  "stale",
		RefreshToken: "rtk-old",
		ExpiresAt:    time.Now().Add(-10 * time.Second),
	})
	src := NewTokenSource(path)
	src.SetIssuer(iss.srv.URL)
	src.SetHTTPClient(iss.srv.Client())

	got, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh-atk" {
		t.Errorf("got %q, want fresh-atk", got)
	}
	if iss.calls.Load() != 1 {
		t.Errorf("issuer calls = %d, want 1", iss.calls.Load())
	}
	// Reload from disk: the new token should have been persisted.
	persisted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AccessToken != "fresh-atk" {
		t.Errorf("persisted access = %q, want fresh-atk", persisted.AccessToken)
	}
}

func TestTokenSourceForceRefresh(t *testing.T) {
	iss := newFakeIssuer(t)
	dir := t.TempDir()
	// not-yet-expired token; ForceRefresh should still hit the issuer.
	path := writeStore(t, dir, TokenSet{
		AccessToken:  "still-good",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	src := NewTokenSource(path)
	src.SetIssuer(iss.srv.URL)
	src.SetHTTPClient(iss.srv.Client())

	if err := src.ForceRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "fresh-atk" {
		t.Errorf("got %q, want fresh-atk", tok)
	}
	if iss.calls.Load() != 1 {
		t.Errorf("issuer calls = %d, want 1", iss.calls.Load())
	}
}

func TestTokenSourceSingleFlight(t *testing.T) {
	// 16 concurrent goroutines on an expired token — the issuer must
	// see at most one refresh, not 16.
	iss := newFakeIssuer(t)
	dir := t.TempDir()
	path := writeStore(t, dir, TokenSet{
		AccessToken:  "stale",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	src := NewTokenSource(path)
	src.SetIssuer(iss.srv.URL)
	src.SetHTTPClient(iss.srv.Client())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := src.Token(context.Background())
			if err != nil {
				t.Errorf("Token: %v", err)
			}
			if tok != "fresh-atk" {
				t.Errorf("got %q, want fresh-atk", tok)
			}
		}()
	}
	wg.Wait()
	if got := iss.calls.Load(); got != 1 {
		t.Errorf("issuer calls = %d, want 1 (single-flight failed)", got)
	}
}

func TestTokenSourceNoRefreshToken(t *testing.T) {
	dir := t.TempDir()
	path := writeStore(t, dir, TokenSet{
		AccessToken: "stale",
		// RefreshToken intentionally empty
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	src := NewTokenSource(path)
	_, err := src.Token(context.Background())
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("got %v, want ErrNoRefreshToken", err)
	}
}

func TestTokenSourceCurrentDoesNotRefresh(t *testing.T) {
	iss := newFakeIssuer(t)
	dir := t.TempDir()
	path := writeStore(t, dir, TokenSet{
		AccessToken:  "stale",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Email:        "p@example.com",
	})
	src := NewTokenSource(path)
	src.SetIssuer(iss.srv.URL)
	src.SetHTTPClient(iss.srv.Client())

	cur, err := src.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cur.AccessToken != "stale" || cur.Email != "p@example.com" {
		t.Errorf("Current returned wrong cached set: %+v", cur)
	}
	if iss.calls.Load() != 0 {
		t.Errorf("issuer calls = %d, want 0 (Current must not refresh)", iss.calls.Load())
	}
}
