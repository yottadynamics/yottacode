package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeIdP stands up a minimal fake authorization server (RFC 8414
// metadata + /token) and a fake MCP resource server (RFC 9728 protected-
// resource metadata at the well-known path the SDK's discovery tries
// first — confirmed against Google's real gmailmcp.googleapis.com
// endpoint during the review that motivated this feature) so the OAuth
// flow can be exercised end to end without a real IdP or a real browser.
type fakeIdP struct {
	auth        *httptest.Server
	resource    *httptest.Server
	resourceURL string

	tokenResourceParam atomic.Value // string, set by the last /token request
	tokenCalls         atomic.Int64
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{}

	authMux := http.NewServeMux()
	authMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.auth.URL,
			"authorization_endpoint":                f.auth.URL + "/authorize",
			"token_endpoint":                        f.auth.URL + "/token",
			"response_types_supported":              []string{"code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		})
	})
	authMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.tokenResourceParam.Store(r.FormValue("resource"))
		f.tokenCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("AT-%d", f.tokenCalls.Load()),
			"token_type":    "Bearer",
			"refresh_token": "RT-1",
			"expires_in":    3600,
		})
	})
	f.auth = httptest.NewServer(authMux)
	t.Cleanup(f.auth.Close)

	resourceMux := http.NewServeMux()
	resourceMux.HandleFunc("/.well-known/oauth-protected-resource/mcp/v1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"resource":              f.resourceURL,
			"authorization_servers": []string{f.auth.URL},
		})
	})
	f.resource = httptest.NewServer(resourceMux)
	t.Cleanup(f.resource.Close)
	f.resourceURL = f.resource.URL + "/mcp/v1"

	return f
}

// simulateBrowserRedirect hits the loopback callback the way a real
// browser would after the user completes sign-in at authURL, extracting
// state from it (the SDK generates and validates state itself; the test
// only needs to echo it back, same as a real authorization server would).
func simulateBrowserRedirect(t *testing.T, authURL, code string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("auth URL has no state param: %s", authURL)
	}
	cb := fmt.Sprintf("http://%s:%s%s?code=%s&state=%s", oauthLoopbackHost, oauthLoopbackPort, oauthLoopbackPath, code, state)
	// The loopback server may not have finished binding the instant
	// StartOAuthLogin's onAuthURL fires (it fires from inside the
	// fetcher, which binds the listener before calling onAuthURL — see
	// oauthAuthorizationCodeFetcher — so this should already be up, but
	// retry briefly to avoid a flaky race under load).
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get(cb)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("simulate browser redirect: %v", err)
	}
	resp.Body.Close()
}

func TestOAuth_ResourceIndicatorOnAuthorizeAndToken(t *testing.T) {
	openBrowserFunc = func(string) error { return nil }
	t.Cleanup(func() { openBrowserFunc = openBrowser })
	t.Setenv("HOME", t.TempDir())

	f := newFakeIdP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pending, err := StartOAuthLogin(ctx, "gmail", f.resourceURL, OAuthOptions{ClientID: "test-client"}, f.resource.Client())
	if err != nil {
		t.Fatalf("StartOAuthLogin: %v", err)
	}

	authURL, err := url.Parse(pending.AuthURL)
	if err != nil {
		t.Fatalf("parse pending.AuthURL: %v", err)
	}
	if got := authURL.Query().Get("resource"); got != f.resourceURL {
		t.Errorf("authorize URL resource param = %q, want %q (RFC 8707)", got, f.resourceURL)
	}

	simulateBrowserRedirect(t, pending.AuthURL, "test-auth-code")

	tok, err := pending.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if tok.AccessToken == "" {
		t.Fatal("expected a non-empty access token")
	}

	// The single most important acceptance gate (roadmap/v0.5.0/
	// k5-mcp-c4-oauth-elicitation.md): every token request must carry
	// the RFC 8707 resource indicator for the specific MCP URL.
	got, _ := f.tokenResourceParam.Load().(string)
	if got != f.resourceURL {
		t.Errorf("token request resource param = %q, want %q (RFC 8707)", got, f.resourceURL)
	}

	// The token must already be on disk — a later session reuses it
	// without re-prompting (see TestOAuth_ReusesPersistedTokenWithoutReprompt).
	stored, err := LoadToken("gmail")
	if err != nil || stored == nil || stored.AccessToken != tok.AccessToken {
		t.Errorf("LoadToken(gmail) after successful sign-in = (%+v, %v), want the issued token persisted", stored, err)
	}
}

func TestOAuth_LoginFailurePropagatesFromCallback(t *testing.T) {
	openBrowserFunc = func(string) error { return nil }
	t.Cleanup(func() { openBrowserFunc = openBrowser })
	t.Setenv("HOME", t.TempDir())

	f := newFakeIdP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pending, err := StartOAuthLogin(ctx, "gmail", f.resourceURL, OAuthOptions{ClientID: "test-client"}, f.resource.Client())
	if err != nil {
		t.Fatalf("StartOAuthLogin: %v", err)
	}

	u, _ := url.Parse(pending.AuthURL)
	state := u.Query().Get("state")
	cb := fmt.Sprintf("http://%s:%s%s?error=access_denied&error_description=user+declined&state=%s",
		oauthLoopbackHost, oauthLoopbackPort, oauthLoopbackPath, state)
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatalf("simulate denied redirect: %v", err)
	}
	resp.Body.Close()

	if _, err := pending.Wait(ctx); err == nil {
		t.Fatal("expected Wait to return an error for a denied authorization")
	}

	if got, _ := LoadToken("gmail"); got != nil {
		t.Errorf("a failed sign-in must not leave a token on disk, got %+v", got)
	}
}

func TestOAuth_ReusesPersistedTokenWithoutReprompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeIdP(t)

	stored := &oauth2.Token{
		AccessToken:  "cached-at",
		RefreshToken: "cached-rt",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
	if err := SaveToken("gmail", stored); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	handler, err := newOAuthHandler("gmail", f.resourceURL, OAuthOptions{ClientID: "test-client"}, f.resource.Client(), nil)
	if err != nil {
		t.Fatalf("newOAuthHandler: %v", err)
	}

	// TokenSource must already be populated from the persisted token —
	// no Authorize() call, no browser, no loopback listener — for a
	// second session to "just work" against a still-valid credential.
	ts, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if ts == nil {
		t.Fatal("TokenSource is nil — a persisted refresh token should have been wired as InitialTokenSource")
	}
	got, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got.AccessToken != "cached-at" {
		t.Errorf("reused token AccessToken = %q, want %q", got.AccessToken, "cached-at")
	}
}

// TestHTTPClient_OAuthLiveConnectionNeverOpensBrowser guards the fix for
// the production-readiness gap found in review: HTTPClient.Start runs
// unattended (session startup happens in the background; a tool call
// can happen mid-turn with no human watching), so its OAuthHandler must
// never run the interactive flow — no browser, no blocking on a human —
// even when the server demands auth and no token is on file. It must
// instead fail fast with guidance to run `/mcp auth <name>`, well under
// InitializeTimeout (30s), not time out looking like a generic connect
// failure. See nonInteractiveOAuthHandler's doc comment.
func TestHTTPClient_OAuthLiveConnectionNeverOpensBrowser(t *testing.T) {
	openBrowserFunc = func(string) error {
		t.Error("a live MCP connection must never open a browser — that's the /mcp auth-only interactive path")
		return nil
	}
	t.Cleanup(func() { openBrowserFunc = openBrowser })
	t.Setenv("HOME", t.TempDir())

	// Always-401, no protected-resource metadata registered: the
	// simplest server shape that demands auth. HTTPClient's oauth
	// wiring must handle this without ever reaching for a browser.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	c := NewHTTPClient("gmail", ts.URL, nil, false, Policy{}, "", &OAuthOptions{ClientID: "test-client"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := c.Start(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Start should fail when the server requires auth and no token is on file")
	}
	if !strings.Contains(err.Error(), "not authenticated") || !strings.Contains(err.Error(), "/mcp auth gmail") {
		t.Errorf("error should guide the user to `/mcp auth gmail`; got %q", err)
	}
	if elapsed >= InitializeTimeout {
		t.Errorf("Start took %v — should fail fast on the 401, not ride out InitializeTimeout (%v) waiting on an interactive flow that never runs", elapsed, InitializeTimeout)
	}
}

func TestOAuth_NoStoredTokenLeavesHandlerUnauthenticated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeIdP(t)

	handler, err := newOAuthHandler("gmail", f.resourceURL, OAuthOptions{ClientID: "test-client"}, f.resource.Client(), nil)
	if err != nil {
		t.Fatalf("newOAuthHandler: %v", err)
	}
	ts, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if ts != nil {
		t.Error("TokenSource should be nil with no persisted token and no Authorize() call yet — the transport is expected to hit 401 and trigger Authorize")
	}
}
