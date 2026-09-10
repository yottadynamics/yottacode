package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// OAuth 2.1 authorization-code + PKCE support for auth = "oauth" MCP
// servers (roadmap/v0.5.0/k5-mcp-c4-oauth-elicitation.md). The protocol
// machinery — RFC 8707 resource indicators, RFC 9728 protected-resource
// discovery, PKCE, dynamic/pre-registered client registration, refresh —
// all comes from the pinned github.com/modelcontextprotocol/go-sdk's
// auth/ and oauthex/ packages; this file is the glue that wires it into
// HTTPClient (see http_client.go's oauth field) and persists tokens
// across sessions (oauth_store.go).
//
// Two distinct call paths use this machinery, deliberately kept apart:
//   - The interactive flow (StartOAuthLogin/RunOAuthLogin, built on the
//     full *sdkauth.AuthorizationCodeHandler from newOAuthHandler) opens
//     a browser and blocks on a human. Reserved for /mcp auth and
//     `yottacode mcp auth` — an explicit, foreground user action.
//   - The live session's transport (HTTPClient.Start) gets that same
//     handler wrapped in nonInteractiveOAuthHandler, which keeps silent
//     token reuse/refresh working but fails fast instead of running the
//     interactive flow. Start() runs unattended in the background at
//     every session launch, and a tool call can run mid-turn with no
//     human watching — neither may pop a browser.

// OAuthOptions carries the per-server OAuth configuration HTTPClient needs
// to build a sdkauth.OAuthHandler. Populated from config.MCPServer by
// Manager.newClient; nil means auth != "oauth" (no handler is built).
type OAuthOptions struct {
	// ClientID is the MCP client's identifier, pre-registered with the
	// server's authorization server. Already $VAR-expanded by the time
	// this struct is built — see manager.go.
	ClientID string
	// ClientSecret pairs with ClientID; empty for a true public client.
	// Already $VAR-expanded.
	ClientSecret string
	// Scopes, when non-empty, is requested explicitly instead of the
	// server's full scopes_supported.
	Scopes []string
}

// oauthLoopbackHost/Port/Path fix the redirect_uri every yottacode MCP
// OAuth login uses. Fixed (not ephemeral) because
// sdkauth.AuthorizationCodeHandlerConfig.RedirectURL is set once at
// handler-construction time and reused for the handler's whole life
// (including a later step-up re-authorization) — an ephemeral port
// chosen per-Authorize call would require rebuilding the handler (and
// re-registering the client) every time. The fetcher binds this address
// fresh for each Authorize call and releases it immediately after (see
// oauthAuthorizationCodeFetcher), so it's only held for the duration of
// an actual sign-in, not for the server's whole session lifetime.
// Distinct from openai's DefaultRedirectURI port (1455) so the two flows
// never collide if both happen to run at once.
const (
	oauthLoopbackHost = "127.0.0.1"
	oauthLoopbackPort = "51763"
	oauthLoopbackPath = "/callback"
)

var oauthRedirectURI = fmt.Sprintf("http://%s:%s%s", oauthLoopbackHost, oauthLoopbackPort, oauthLoopbackPath)

// newOAuthHandler builds the sdkauth.OAuthHandler HTTPClient.Start wires
// into StreamableClientTransport.OAuthHandler when a server's Auth is
// "oauth". discoveryClient is used for every OAuth-protocol HTTP call
// (protected-resource metadata, auth-server metadata, client
// registration, token exchange/refresh) — callers pass the same
// policy-hardened transport buildTransport() constructs for the MCP
// connection itself, WITHOUT the static-header wrapper (those headers
// are for the MCP endpoint, not the authorization server). onAuthURL, if
// non-nil, is invoked with the authorize URL the instant it's built —
// before the fetcher blocks on the user completing sign-in — so a
// caller (StartOAuthLogin, or the lazy first-401 path) can surface it.
func newOAuthHandler(serverName, serverURL string, opts OAuthOptions, discoveryClient *http.Client, onAuthURL func(string)) (*sdkauth.AuthorizationCodeHandler, error) {
	if strings.TrimSpace(opts.ClientID) == "" {
		return nil, fmt.Errorf("mcp(%s): auth=oauth requires oauth_client_id", serverName)
	}
	creds := &oauthex.ClientCredentials{ClientID: opts.ClientID}
	if opts.ClientSecret != "" {
		creds.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: opts.ClientSecret}
	}

	handlerCfg := &sdkauth.AuthorizationCodeHandlerConfig{
		PreregisteredClient:      creds,
		RedirectURL:              oauthRedirectURI,
		AuthorizationCodeFetcher: oauthAuthorizationCodeFetcher(serverName, onAuthURL),
		RequestRefreshToken:      true,
		Client:                   discoveryClient,
		NewTokenSource: func(ctx context.Context, oa2 *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			// initial is nil here (not tok): this path runs right after a
			// FRESH exchange that has never been written to disk, so the
			// very first Token() call — made internally by Authorize's own
			// updateGrantedScopes — must trigger a save. Passing tok as
			// initial would pre-seed lastAT to that same value and make
			// that first call look like a no-op change, silently skipping
			// the save entirely (caught by
			// TestOAuth_ResourceIndicatorOnAuthorizeAndToken's persistence
			// assertion). Contrast newOAuthHandler's InitialTokenSource
			// wiring below, which correctly passes the loaded token as
			// initial — that one WAS already on disk.
			return newPersistingTokenSource(serverName, oa2.TokenSource(ctx, tok), nil), nil
		},
	}

	// Reuse a persisted refresh token across sessions without
	// re-prompting: wire it up as InitialTokenSource, wrapped in a
	// refreshing oauth2.TokenSource built from the server's real token
	// endpoint. Without this, TokenSource would start nil and every new
	// process would take the interactive Authorize() path even with a
	// perfectly good refresh token on disk. A discovery failure here
	// just means we fall back to that interactive path instead of
	// failing Start outright — not fatal.
	if stored, err := LoadToken(serverName); err == nil && stored != nil {
		if oa2Cfg, derr := discoverOAuth2Config(context.Background(), serverURL, creds, discoveryClient); derr == nil {
			handlerCfg.InitialTokenSource = newPersistingTokenSource(serverName, oa2Cfg.TokenSource(context.Background(), stored), stored)
		}
	}

	return sdkauth.NewAuthorizationCodeHandler(handlerCfg)
}

// nonInteractiveOAuthHandler wraps the full interactive
// *sdkauth.AuthorizationCodeHandler for use on the LIVE session
// transport (HTTPClient.Start's StreamableClientTransport.OAuthHandler).
//
// The SDK's contract is: the transport calls TokenSource before every
// request to inject the bearer header, and calls Authorize — which
// opens a browser and blocks on a human — whenever a request comes back
// 401/403. That's fine for TokenSource: a persisted, still-valid (or
// silently refreshable via its stored refresh token) credential keeps
// working exactly as the full handler would. It is NOT fine for
// Authorize: Start() runs unattended in the background at every session
// launch (see manager.go's Start, called from the TUI's non-blocking
// MCP startup), and CallTool can run mid-agent-turn with no human
// watching. Neither is a place a browser should silently pop open —
// especially since HTTPClient.Start's own InitializeTimeout (30s,
// stdio_client.go) would almost always time out a real interactive
// login anyway, turning "needs auth" into a confusing connect-timeout
// instead of an actionable message.
//
// So this wrapper delegates TokenSource straight through and overrides
// only Authorize: instead of running the interactive flow, it fails
// immediately with guidance to run `/mcp auth <name>` — the one place
// (StartOAuthLogin / RunOAuthLogin, called from the /mcp auth and
// `mcp auth` commands) the interactive flow is allowed to run, because
// that's the one place it was the user's own explicit action.
type nonInteractiveOAuthHandler struct {
	inner sdkauth.OAuthHandler
	name  string
}

var _ sdkauth.OAuthHandler = (*nonInteractiveOAuthHandler)(nil)

func (h *nonInteractiveOAuthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.inner.TokenSource(ctx)
}

func (h *nonInteractiveOAuthHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	// Contract from auth.OAuthHandler's doc comment: the implementation
	// is responsible for closing the response body (its headers are
	// available to it, but by the time Authorize is called the body has
	// already been consumed for the caller's own error-detection).
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return fmt.Errorf("mcp(%s): not authenticated (or the stored token was rejected) — run `/mcp auth %s` to sign in", h.name, h.name)
}

// discoverOAuth2Config runs the same protected-resource + authorization-
// server metadata discovery sdkauth.AuthorizationCodeHandler.Authorize
// does internally, but exposed standalone so newOAuthHandler can build a
// refreshing token source for a token it already has — before any 401
// has happened, which is the only time Authorize itself runs discovery.
func discoverOAuth2Config(ctx context.Context, serverURL string, creds *oauthex.ClientCredentials, httpClient *http.Client) (*oauth2.Config, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server url: %w", err)
	}
	prmURL := *u
	prmURL.Path = "/.well-known/oauth-protected-resource" + u.Path
	prm, err := oauthex.GetProtectedResourceMetadata(ctx, prmURL.String(), serverURL, httpClient)
	if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
		rootURL := *u
		rootURL.Path = "/.well-known/oauth-protected-resource"
		rootResource := *u
		rootResource.Path = ""
		prm, err = oauthex.GetProtectedResourceMetadata(ctx, rootURL.String(), rootResource.String(), httpClient)
		if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
			return nil, fmt.Errorf("discover protected resource metadata: %w", err)
		}
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, prm.AuthorizationServers[0], httpClient)
	if err != nil {
		return nil, fmt.Errorf("discover authorization server metadata: %w", err)
	}
	if asm == nil {
		return nil, fmt.Errorf("no authorization server metadata at %s", prm.AuthorizationServers[0])
	}
	clientSecret := ""
	if creds.ClientSecretAuth != nil {
		clientSecret = creds.ClientSecretAuth.ClientSecret
	}
	return &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: clientSecret,
		// AutoDetect: the SDK's own client-secret-auth-style preference
		// (client_secret_post > client_secret_basic) is unexported, and
		// this path only ever drives a refresh-token request, where
		// x/oauth2 probing the style once and caching it is cheap and
		// safe — unlike the initial code exchange, a failed refresh
		// just falls back to the interactive Authorize() path.
		Endpoint: oauth2.Endpoint{AuthURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint, AuthStyle: oauth2.AuthStyleAutoDetect},
	}, nil
}

// persistingTokenSource wraps an oauth2.TokenSource and writes every token
// it hands back to SaveToken — the initial exchange (called once from
// inside sdkauth.AuthorizationCodeHandler.Authorize's own bookkeeping) and
// every silent refresh thereafter. lastAccess dedupes repeat saves of an
// unchanged token (e.g. the InitialTokenSource path's first call, which
// just re-hands-back what LoadToken already read from the same file).
type persistingTokenSource struct {
	name   string
	inner  oauth2.TokenSource
	mu     sync.Mutex
	lastAT string
}

func newPersistingTokenSource(name string, inner oauth2.TokenSource, initial *oauth2.Token) oauth2.TokenSource {
	ts := &persistingTokenSource{name: name, inner: inner}
	if initial != nil {
		ts.lastAT = initial.AccessToken
	}
	return ts
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	changed := tok.AccessToken != p.lastAT
	if changed {
		p.lastAT = tok.AccessToken
	}
	p.mu.Unlock()
	if changed {
		// Best-effort: a disk write failure shouldn't break the live
		// connection, only cross-session reuse — the next LoadToken
		// just misses and the user re-authenticates.
		_ = SaveToken(p.name, tok)
	}
	return tok, nil
}

// oauthAuthorizationCodeFetcher returns the sdkauth.AuthorizationCodeFetcher
// used for every Authorize() call on serverName's handler: surface the
// auth URL (via onAuthURL, if set), best-effort open the browser, bind a
// fresh loopback listener on the fixed redirect address, and wait for the
// single redirect hit or ctx cancellation.
func oauthAuthorizationCodeFetcher(serverName string, onAuthURL func(string)) sdkauth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
		authURL := googleOfflineAccessURL(args.URL)

		listener, err := net.Listen("tcp", net.JoinHostPort(oauthLoopbackHost, oauthLoopbackPort))
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return nil, fmt.Errorf("mcp(%s): oauth callback port %s is already in use — "+
					"another MCP sign-in may be in progress; finish or cancel it and retry", serverName, oauthLoopbackPort)
			}
			return nil, fmt.Errorf("mcp(%s): oauth callback: listen: %w", serverName, err)
		}
		defer listener.Close()

		type callbackResult struct {
			code, state, iss string
			err              error
		}
		resultCh := make(chan callbackResult, 1)
		mux := http.NewServeMux()
		mux.HandleFunc(oauthLoopbackPath, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			res := callbackResult{code: q.Get("code"), state: q.Get("state"), iss: q.Get("iss")}
			if errParam := q.Get("error"); errParam != "" {
				res.err = fmt.Errorf("oauth: %s: %s", errParam, q.Get("error_description"))
			} else if res.code == "" {
				res.err = errors.New("oauth: callback missing code parameter")
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if res.err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "yottacode: sign-in to %s failed: %s\nReturn to the terminal for details.\n", serverName, res.err)
			} else {
				fmt.Fprintf(w, "yottacode: signed in to %s.\nYou can close this tab and return to the terminal.\n", serverName)
			}
			select {
			case resultCh <- res:
			default:
			}
		})
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(listener) }()
		defer srv.Close()

		if onAuthURL != nil {
			onAuthURL(authURL)
		}
		_ = openBrowserFunc(authURL)

		select {
		case res := <-resultCh:
			if res.err != nil {
				return nil, res.err
			}
			return &sdkauth.AuthorizationResult{Code: res.code, State: res.state, Iss: res.iss}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// googleOfflineAccessURL appends access_type=offline&prompt=consent to a
// Google authorization URL. Google doesn't support SEP-2207's
// offline_access-scope convention for requesting a refresh token (its
// auth-server metadata never advertises "offline_access" in
// scopes_supported — confirmed against Google's real discovery document);
// it uses these two query params instead. Without them Google issues an
// access token only, so a persisted "refresh token" would in fact be
// empty and every new process would need an interactive re-login roughly
// hourly. Detected by authorization_endpoint host rather than a config
// flag — it's inferable from what discovery already returned, and every
// Google Workspace MCP server shares the same authorization_endpoint.
// A no-op for any non-Google authorization server.
func googleOfflineAccessURL(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil || u.Hostname() != "accounts.google.com" {
		return authURL
	}
	q := u.Query()
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	u.RawQuery = q.Encode()
	return u.String()
}

// openBrowserFunc is a var (not a direct call to openBrowser) so tests can
// replace it and assert on the URL without actually spawning a browser
// process — every real caller leaves it at the default.
var openBrowserFunc = openBrowser

// openBrowser launches the user's default browser at url, best-effort —
// a failure here just means the caller's printed/surfaced URL is the
// user's only path in, not a fatal error. Mirrors
// internal/auth/openai.OpenBrowser; kept as its own small copy rather
// than importing that package, since the only thing shared is this
// generic platform dispatch.
func openBrowser(url string) error {
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

// PendingOAuthLogin is an in-flight interactive OAuth sign-in: the
// authorize URL is known and surfaced, but the user hasn't finished (or
// the loopback callback hasn't landed) yet. Mirrors
// internal/auth/openai's StartLogin/Wait split so `/mcp auth` can render
// the URL to the transcript immediately, the same way `/provider add`'s
// openai-auth flow does, instead of blocking silently until sign-in
// completes.
type PendingOAuthLogin struct {
	// AuthURL is the URL opened in the browser — surfaced as a fallback
	// in case the launch failed.
	AuthURL string

	resultCh chan oauthLoginResult
}

type oauthLoginResult struct {
	tok *oauth2.Token
	err error
}

// StartOAuthLogin runs the synchronous prep phase of an OAuth sign-in
// for serverName/serverURL: builds the handler, starts Authorize() in the
// background, and returns once the authorize URL is known. Callers MUST
// call Wait (or let the returned PendingOAuthLogin's goroutine finish) to
// observe the result — used by both `yottacode mcp auth` (blocking CLI)
// and the TUI's `/mcp auth` (async tea.Cmd), which is why the URL and the
// completion are two separate steps rather than one blocking call.
func StartOAuthLogin(ctx context.Context, serverName, serverURL string, opts OAuthOptions, discoveryClient *http.Client) (*PendingOAuthLogin, error) {
	urlCh := make(chan string, 1)
	handler, err := newOAuthHandler(serverName, serverURL, opts, discoveryClient, func(u string) {
		select {
		case urlCh <- u:
		default:
		}
	})
	if err != nil {
		return nil, err
	}

	resultCh := make(chan oauthLoginResult, 1)
	go func() {
		req := &http.Request{URL: mustParseURL(serverURL)}
		resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
		if err := handler.Authorize(ctx, req, resp); err != nil {
			resultCh <- oauthLoginResult{err: err}
			return
		}
		ts, _ := handler.TokenSource(ctx)
		if ts == nil {
			resultCh <- oauthLoginResult{err: errors.New("mcp oauth: sign-in completed but no token was issued")}
			return
		}
		tok, err := ts.Token()
		resultCh <- oauthLoginResult{tok: tok, err: err}
	}()

	select {
	case u := <-urlCh:
		return &PendingOAuthLogin{AuthURL: u, resultCh: resultCh}, nil
	case r := <-resultCh:
		// Authorize() failed before ever building an auth URL (e.g. discovery
		// or client registration failed) — surface that directly instead of
		// blocking forever on a URL that will never arrive.
		return nil, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Wait blocks until the sign-in the caller started with StartOAuthLogin
// completes (or ctx is canceled). The token is already persisted to disk
// by the time this returns successfully — see persistingTokenSource.
func (p *PendingOAuthLogin) Wait(ctx context.Context) (*oauth2.Token, error) {
	select {
	case r := <-p.resultCh:
		return r.tok, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RunOAuthLogin runs StartOAuthLogin then Wait synchronously — the thin
// wrapper `yottacode mcp auth` uses. onAuthURL is called once the URL is
// known, before this blocks on the rest of the flow (same contract as
// StartOAuthLogin's internal callback).
func RunOAuthLogin(ctx context.Context, serverName, serverURL string, opts OAuthOptions, discoveryClient *http.Client, onAuthURL func(string)) (*oauth2.Token, error) {
	pending, err := StartOAuthLogin(ctx, serverName, serverURL, opts, discoveryClient)
	if err != nil {
		return nil, err
	}
	if onAuthURL != nil {
		onAuthURL(pending.AuthURL)
	}
	return pending.Wait(ctx)
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		// serverURL is already validated by config.Validate and Policy.CheckURL
		// before any oauth code path runs — an unparseable URL here would mean
		// those checks were bypassed, which is a programming error, not a
		// runtime condition to handle gracefully.
		return &url.URL{}
	}
	return u
}

// oauthExpansionWarnings mirrors headerExpansionWarnings for the two
// oauth credential fields: one warning per $VAR reference that isn't set
// in yottacode's process environment.
func oauthExpansionWarnings(name, clientID, clientSecret string) []string {
	var out []string
	for _, field := range []struct {
		label, raw string
	}{{"oauth_client_id", clientID}, {"oauth_client_secret", clientSecret}} {
		for _, varName := range referencedVars(field.raw) {
			if _, set := os.LookupEnv(varName); !set {
				out = append(out, fmt.Sprintf("mcp(%s): %s references $%s which is unset; it will be sent empty",
					name, field.label, varName))
			}
		}
	}
	return out
}
