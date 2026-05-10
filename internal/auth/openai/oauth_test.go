package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExchangeCodeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostForm.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %s", got)
		}
		if got := r.PostForm.Get("code"); got != "the-code" {
			t.Errorf("code = %s", got)
		}
		if got := r.PostForm.Get("code_verifier"); got != "the-verifier" {
			t.Errorf("code_verifier = %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token":"atk",
			"refresh_token":"rtk",
			"token_type":"bearer",
			"expires_in":3600
		}`))
	}))
	t.Cleanup(srv.Close)
	ts, err := ExchangeCode(context.Background(), srv.Client(), srv.URL, "client", "http://127.0.0.1:1455/auth/callback", "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "atk" || ts.RefreshToken != "rtk" {
		t.Errorf("tokens: %+v", ts)
	}
	if ts.ExpiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Errorf("expires_at not in future: %v", ts.ExpiresAt)
	}
}

func TestExchangeCodeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code expired"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := ExchangeCode(context.Background(), srv.Client(), srv.URL, "c", "http://127.0.0.1:1455/auth/callback", "x", "v")
	if err == nil || !strings.Contains(err.Error(), "code expired") {
		t.Errorf("got %v, want error mentioning 'code expired'", err)
	}
}

func TestRefreshTokenPreservesRefreshOnEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		// Server omits refresh_token (no rotation); helper should
		// echo back the input value so the stored set stays coherent.
		_, _ = w.Write([]byte(`{"access_token":"atk2","token_type":"bearer","expires_in":1800}`))
	}))
	t.Cleanup(srv.Close)
	ts, err := RefreshToken(context.Background(), srv.Client(), srv.URL, "c", "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "atk2" {
		t.Errorf("access: %s", ts.AccessToken)
	}
	if ts.RefreshToken != "old-refresh" {
		t.Errorf("refresh = %s, want preserved", ts.RefreshToken)
	}
}

func TestRefreshFlowNoToken(t *testing.T) {
	_, err := Refresh(context.Background(), TokenSet{}, LoginOptions{})
	if err != ErrNoRefreshToken {
		t.Errorf("got %v, want ErrNoRefreshToken", err)
	}
}

func TestAuthURL(t *testing.T) {
	u := AuthURL("https://auth.openai.com/", "client_x", "http://localhost:1455/auth/callback", "state-1", "challenge-1", "codex_cli_rs", []string{"openid", "profile"})
	for _, want := range []string{
		"https://auth.openai.com/oauth/authorize?",
		"client_id=client_x",
		"code_challenge=challenge-1",
		"code_challenge_method=S256",
		"state=state-1",
		"scope=openid+profile",
		"response_type=code",
		// Codex-specific extras the auth server requires.
		"id_token_add_organizations=true",
		"codex_cli_simplified_flow=true",
		"originator=codex_cli_rs",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("AuthURL missing %q in %s", want, u)
		}
	}
}

func TestAuthURLDefaultOriginator(t *testing.T) {
	// Empty originator argument should fall back to the default.
	u := AuthURL("https://auth.openai.com", "c", "http://localhost:1455/auth/callback", "s", "ch", "", []string{"openid"})
	if !strings.Contains(u, "originator=codex_cli_rs") {
		t.Errorf("expected default originator in %s", u)
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Now()
	if !(TokenSet{ExpiresAt: now.Add(-time.Second)}).IsExpired(0) {
		t.Error("past should be expired")
	}
	if (TokenSet{ExpiresAt: now.Add(time.Hour)}).IsExpired(0) {
		t.Error("future should not be expired")
	}
	if !(TokenSet{ExpiresAt: now.Add(time.Minute)}).IsExpired(2 * time.Minute) {
		t.Error("with leeway, near-future should count as expired")
	}
	if (TokenSet{}).IsExpired(0) {
		t.Error("zero ExpiresAt should not be expired")
	}
}

// postToken caps the body read at 64 KiB to match the codebase's
// defensive style. Issue raised in review: an unbounded io.ReadAll
// against a (trusted but) network-controlled body is the wrong
// shape for this codebase. Test asserts oversized bodies don't
// blow up memory and either truncate cleanly (parse error) or are
// rejected.
func TestPostToken_BoundsBodyRead(t *testing.T) {
	// Build a body well over 64 KiB. The cap means the reader stops
	// before reading it all; whatever JSON the truncated prefix forms
	// will fail to parse, surfacing as an error rather than a memory
	// pressure event. Either outcome is acceptable; what we lock is
	// "doesn't OOM, returns gracefully."
	pad := strings.Repeat("a", 80*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"x","refresh_token":"y","token_type":"bearer","expires_in":3600,"padding":"` + pad + `"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := ExchangeCode(context.Background(), srv.Client(), srv.URL, "c", "http://localhost:1455/auth/callback", "x", "v")
	// We expect a decode error because the JSON is truncated
	// mid-padding by the LimitReader cap. The exact error wording
	// is incidental — what matters is we returned cleanly.
	if err == nil {
		t.Errorf("expected error from truncated 80 KiB body; got nil")
	}
}
