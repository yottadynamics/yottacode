package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Codex CLI public client constants. Embedded directly because they
// are public (anyone can inspect a compiled Codex binary) and
// pulling them from config would just defer the same discoverability.
//
// The redirect host MUST be "localhost" — not 127.0.0.1. OpenAI's
// auth server has the literal string allow-listed; using the IP
// surfaces as a generic "unknown_error" page mid-flow.
const (
	DefaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultIssuerURL   = "https://auth.openai.com"
	DefaultRedirectURI = "http://localhost:1455/auth/callback"
	DefaultOriginator  = "codex_cli_rs"
)

// DefaultScopes mirrors the Codex CLI flow exactly. offline_access
// gets us a refresh_token; api.connectors.read/invoke gate the
// downstream ChatGPT-backend API calls. OpenAI's auth server does
// not return granted scopes in a stable way, so any discrepancy
// here surfaces as opaque downstream 401s rather than a clean
// scope-denied error — match Codex byte-for-byte to avoid drift.
var DefaultScopes = []string{
	"openid",
	"profile",
	"email",
	"offline_access",
	"api.connectors.read",
	"api.connectors.invoke",
}

// AuthURL builds the authorize-endpoint URL for one PKCE flow. The
// caller is expected to open this URL in the user's browser.
//
// originator and the codex_cli_simplified_flow / id_token_add_organizations
// flags are not standard OAuth — they are Codex-specific knobs the
// auth server requires when the request comes from the public Codex
// client. Omitting any of them yields "unknown_error".
func AuthURL(issuer, clientID, redirectURI, state, codeChallenge, originator string, scopes []string) string {
	if originator == "" {
		originator = DefaultOriginator
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("state", state)
	q.Set("originator", originator)
	return strings.TrimRight(issuer, "/") + "/oauth/authorize?" + q.Encode()
}

// tokenResponse is the JSON wire shape of /oauth/token responses
// (both authorization_code and refresh_token grants).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// ExchangeCode trades an authorization code for an access + refresh
// token pair via /oauth/token.
func ExchangeCode(ctx context.Context, httpClient *http.Client, issuer, clientID, redirectURI, code, codeVerifier string) (TokenSet, error) {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("client_id", clientID)
	v.Set("code", code)
	v.Set("redirect_uri", redirectURI)
	v.Set("code_verifier", codeVerifier)
	return postToken(ctx, httpClient, issuer, v)
}

// RefreshToken exchanges a refresh token for a fresh access token.
// The auth server may rotate the refresh token; if the response
// omits one, we echo the input back so the caller's stored value
// stays coherent.
func RefreshToken(ctx context.Context, httpClient *http.Client, issuer, clientID, refreshToken string) (TokenSet, error) {
	v := url.Values{}
	v.Set("grant_type", "refresh_token")
	v.Set("client_id", clientID)
	v.Set("refresh_token", refreshToken)
	ts, err := postToken(ctx, httpClient, issuer, v)
	if err != nil {
		return TokenSet{}, err
	}
	if ts.RefreshToken == "" {
		ts.RefreshToken = refreshToken
	}
	return ts, nil
}

func postToken(ctx context.Context, httpClient *http.Client, issuer string, body url.Values) (TokenSet, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	endpoint := strings.TrimRight(issuer, "/") + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return TokenSet{}, err
	}
	defer resp.Body.Close()
	// Bound the read — the token endpoint is trusted but defensive
	// caps are the codebase convention. 64 KiB is well above any
	// real OAuth response (usually a few KB).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return TokenSet{}, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return TokenSet{}, fmt.Errorf("openai-auth: decode token response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return TokenSet{}, fmt.Errorf("openai-auth: token exchange failed (status %d): %s", resp.StatusCode, msg)
	}
	expires := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.ExpiresIn == 0 {
		// Safety net: if the server omits expires_in, assume one
		// hour and rely on refresh-on-401 in the eventual adapter.
		expires = time.Now().Add(time.Hour)
	}
	ts := TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		ExpiresAt:    expires,
	}
	if claims, err := DecodeClaims(tr.AccessToken); err == nil {
		ts.AccountID = claims.Subject
		ts.Email = claims.Email
	}
	return ts, nil
}

// IsExpired reports whether the access token is at or past its
// expiry, accounting for a leeway window. Zero ExpiresAt is treated
// as "not expired" — we don't know, so don't force a refresh.
func (ts TokenSet) IsExpired(leeway time.Duration) bool {
	if ts.ExpiresAt.IsZero() {
		return false
	}
	return !time.Now().Add(leeway).Before(ts.ExpiresAt)
}

// ErrNoRefreshToken is returned by Refresh when the stored token set
// has none — the offline_access scope was not granted at login,
// so the caller must re-run the full flow.
var ErrNoRefreshToken = errors.New("openai-auth: no refresh token; re-login required")
