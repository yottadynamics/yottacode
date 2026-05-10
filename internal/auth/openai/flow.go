package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// LoginOptions tunes the orchestrator. Zero values pick the Codex CLI
// defaults so callers can `Login(ctx, LoginOptions{})` with no boilerplate.
type LoginOptions struct {
	Issuer      string
	ClientID    string
	RedirectURI string
	Scopes      []string

	// Originator goes on the authorize URL as a Codex-specific query
	// parameter the auth server requires. Default "codex_cli_rs" — we
	// reuse the Codex public OAuth client, so we identify as it.
	Originator string

	// HTTPClient overrides the *http.Client used for the token
	// endpoint. Tests inject httptest-backed clients here; production
	// callers leave it nil to use http.DefaultClient.
	HTTPClient *http.Client

	// OpenBrowser, when non-nil, replaces the platform launcher.
	// Tests pass a no-op so the flow can be exercised without
	// spawning a real browser.
	OpenBrowser func(url string) error

	// PreLogin runs once with the authorize URL right before the
	// orchestrator blocks on the callback. Use it to print the URL
	// to stderr so the user can paste it manually if browser launch
	// fails.
	PreLogin func(url string)

	// Timeout caps the wait for the user to complete the browser
	// flow. Zero means no timeout (rely on ctx alone).
	Timeout time.Duration
}

func (o *LoginOptions) defaults() {
	if o.Issuer == "" {
		o.Issuer = DefaultIssuerURL
	}
	if o.ClientID == "" {
		o.ClientID = DefaultClientID
	}
	if o.RedirectURI == "" {
		o.RedirectURI = DefaultRedirectURI
	}
	if len(o.Scopes) == 0 {
		o.Scopes = DefaultScopes
	}
	if o.Originator == "" {
		o.Originator = DefaultOriginator
	}
}

// PendingLogin is an OAuth flow with the synchronous prep done —
// PKCE generated, callback server listening, browser launched. The
// caller blocks on Wait to capture the user's redirect.
//
// Lets a tea.Cmd-based caller (wizard, /provider add) surface the
// auth URL to the user before blocking, so a failed browser launch
// doesn't strand the user with no way to copy the URL.
type PendingLogin struct {
	// AuthURL is the URL the browser was launched with. Surface it
	// to the user as a fallback in case the launcher failed.
	AuthURL string

	server *CallbackServer
	pkce   PKCE
	state  string
	opts   LoginOptions
}

// StartLogin runs the synchronous prep phase: generate PKCE+state,
// start the loopback callback server, build the auth URL, launch
// the browser (best effort). Returns a PendingLogin the caller
// blocks on via Wait. Caller MUST call Wait or Close to release
// the listener.
//
// The two-phase API is for tea.Cmd consumers that want to render
// the auth URL while the user is signing in. Synchronous CLI
// callers use Login (the thin wrapper below).
func StartLogin(ctx context.Context, opts LoginOptions) (*PendingLogin, error) {
	opts.defaults()

	pkce, err := NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := NewState()
	if err != nil {
		return nil, err
	}

	server, err := NewCallbackServer(opts.RedirectURI)
	if err != nil {
		return nil, err
	}
	if err := server.Start(); err != nil {
		return nil, err
	}

	authURL := AuthURL(opts.Issuer, opts.ClientID, opts.RedirectURI, state, pkce.Challenge, opts.Originator, opts.Scopes)
	if opts.PreLogin != nil {
		opts.PreLogin(authURL)
	}
	open := opts.OpenBrowser
	if open == nil {
		open = OpenBrowser
	}
	// Browser launch is best-effort. If it fails we still want to
	// keep the callback server up so the user can paste the URL
	// manually — pendingLogin.AuthURL exposes it for that fallback.
	_ = open(authURL)

	return &PendingLogin{
		AuthURL: authURL,
		server:  server,
		pkce:    pkce,
		state:   state,
		opts:    opts,
	}, nil
}

// Wait blocks until the user completes the browser flow and the
// callback server has the auth code, then exchanges it for tokens.
// The server is closed when this returns.
func (p *PendingLogin) Wait(ctx context.Context) (TokenSet, error) {
	defer p.server.Close()

	waitCtx := ctx
	if p.opts.Timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, p.opts.Timeout)
		defer cancel()
	}
	res, err := p.server.Wait(waitCtx)
	if err != nil {
		return TokenSet{}, err
	}
	if res.Err != nil {
		return TokenSet{}, res.Err
	}
	if res.State != p.state {
		return TokenSet{}, errors.New("openai-auth: callback state mismatch (possible CSRF)")
	}
	return ExchangeCode(ctx, p.opts.HTTPClient, p.opts.Issuer, p.opts.ClientID, p.opts.RedirectURI, res.Code, p.pkce.Verifier)
}

// Close releases the callback server listener without waiting for
// the user. Used by callers that abort before Wait runs (user
// cancel, context error). Safe to call after Wait — the server's
// own Close is idempotent.
func (p *PendingLogin) Close() {
	if p.server != nil {
		p.server.Close()
	}
}

// Login runs the full PKCE Authorization Code flow synchronously:
// StartLogin → Wait. The thin wrapper preserves the original blocking
// API for cobra commands and the maintainer probe.
func Login(ctx context.Context, opts LoginOptions) (TokenSet, error) {
	p, err := StartLogin(ctx, opts)
	if err != nil {
		return TokenSet{}, err
	}
	return p.Wait(ctx)
}

// Refresh trades the stored refresh token for a fresh access token.
// Returns ErrNoRefreshToken when the stored set has none — caller
// should re-run Login.
func Refresh(ctx context.Context, ts TokenSet, opts LoginOptions) (TokenSet, error) {
	opts.defaults()
	if ts.RefreshToken == "" {
		return TokenSet{}, ErrNoRefreshToken
	}
	return RefreshToken(ctx, opts.HTTPClient, opts.Issuer, opts.ClientID, ts.RefreshToken)
}

// OpenBrowser launches the user's default browser at url. A failure
// here just means we couldn't auto-open it; the caller has already
// printed the URL via PreLogin so the user can paste it manually.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
