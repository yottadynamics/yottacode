package openai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// CallbackResult is what the loopback handler delivered: either the
// authorization code (success) or the OAuth error pair (failure).
type CallbackResult struct {
	Code  string
	State string
	Err   error
}

// CallbackServer runs an HTTP server on the loopback port baked into
// the redirect URI, waits for exactly one /auth/callback hit, and
// returns the parsed result. The server is single-shot — once the
// first matching request lands, subsequent ones get the same
// rendered response but their queries are discarded.
type CallbackServer struct {
	Addr string
	Path string

	listener net.Listener
	server   *http.Server
	result   chan CallbackResult
}

// NewCallbackServer parses redirectURI to derive the listen address
// + path. Only http://127.0.0.1:<port>/<path> (or localhost) is
// accepted: a non-loopback redirect would expose the OAuth code on
// the wire, which the OAuth spec forbids for native apps.
func NewCallbackServer(redirectURI string) (*CallbackServer, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return nil, fmt.Errorf("callback: parse redirect_uri: %w", err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("callback: redirect_uri must be http (loopback), got %q", u.Scheme)
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" {
		return nil, fmt.Errorf("callback: redirect_uri must use loopback host, got %q", host)
	}
	port := u.Port()
	if port == "" {
		return nil, errors.New("callback: redirect_uri must include a port")
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return &CallbackServer{
		Addr:   net.JoinHostPort(host, port),
		Path:   path,
		result: make(chan CallbackResult, 1),
	}, nil
}

// serveHook, when non-nil, runs at the top of Start's serve goroutine
// just before it calls Serve. It is nil in production; the nil-server
// race regression test sets it to park the goroutine and force the
// Close-before-serve ordering deterministically.
var serveHook func()

// Start binds the listener and begins serving. Bind is synchronous
// so callers know whether the port was free before they open the
// browser — racing the bind would surface as a confusing
// "redirect_uri unreachable" message in the user's tab.
func (c *CallbackServer) Start() error {
	l, err := net.Listen("tcp", c.Addr)
	if err != nil {
		// EADDRINUSE here almost always means an earlier sign-in is
		// still holding the loopback port. The redirect port is fixed
		// by OpenAI's allow-list, so it can't be sidestepped with a
		// different one — surface something the user can act on instead
		// of the raw "bind: address already in use".
		if errors.Is(err, syscall.EADDRINUSE) {
			_, port, _ := net.SplitHostPort(c.Addr)
			return fmt.Errorf("callback: port %s is already in use — an earlier openai sign-in may "+
				"still be running (in another yottacode instance, possibly under a different user); "+
				"free it and retry. Find the holder with `sudo lsof -i :%s` (root is needed to see a "+
				"socket another user owns): %w", port, port, err)
		}
		return fmt.Errorf("callback: listen %s: %w", c.Addr, err)
	}
	c.listener = l
	mux := http.NewServeMux()
	mux.HandleFunc(c.Path, c.handle)
	srv := &http.Server{Handler: mux}
	c.server = srv
	// Serve the captured srv, never the c.server field. Close may nil
	// the field — from Wait's deferred Close on the cancel/timeout path,
	// or an abandoned sign-in's teardown — before this goroutine is
	// scheduled, and (*http.Server)(nil).Serve dereferences nil: an
	// unrecovered SIGSEGV in a background goroutine that crashes the whole
	// process. The captured pointer is immune, and Close still stops it
	// via the same object, so Serve returns http.ErrServerClosed cleanly.
	go func() {
		if serveHook != nil {
			serveHook()
		}
		_ = srv.Serve(l)
	}()
	return nil
}

func (c *CallbackServer) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := CallbackResult{
		Code:  q.Get("code"),
		State: q.Get("state"),
	}
	if errParam := q.Get("error"); errParam != "" {
		desc := q.Get("error_description")
		if desc != "" {
			res.Err = fmt.Errorf("oauth: %s: %s", errParam, desc)
		} else {
			res.Err = fmt.Errorf("oauth: %s", errParam)
		}
	} else if res.Code == "" {
		res.Err = errors.New("oauth: callback missing code parameter")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if res.Err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, htmlError, htmlEscape(res.Err.Error()))
	} else {
		fmt.Fprint(w, htmlSuccess)
	}
	select {
	case c.result <- res:
	default:
		// Already delivered; ignore stray hits (page refresh, double
		// callback). Don't block on a full channel.
	}
}

// Wait blocks until the callback fires or ctx is canceled. The
// server is shut down before returning, so the listener port is
// released even on cancel paths.
func (c *CallbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	defer c.Close()
	select {
	case res := <-c.result:
		return res, nil
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	}
}

// Close shuts the server down. Safe to call multiple times.
func (c *CallbackServer) Close() {
	if c.server != nil {
		_ = c.server.Close()
		c.server = nil
	}
	if c.listener != nil {
		_ = c.listener.Close()
		c.listener = nil
	}
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// htmlSuccess is the page the user sees in the browser tab the
// instant the OAuth callback fires. Black background, terminal-ish
// monospace, a green check, and the one sentence the user actually
// needs ("you can close this tab"). Self-contained — no remote
// fonts, no JS — so it renders identically offline and after the
// callback server has shut down.
const htmlSuccess = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>yottacode — signed in</title>
<style>
  *{box-sizing:border-box}
  html,body{height:100%;margin:0}
  body{
    background:#0a0a0a;
    color:#e8e8e8;
    font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;
    padding:2rem;
  }
  .card{
    max-width:520px;width:100%;
    border:1px solid #1f1f1f;border-radius:10px;
    padding:2.5rem 2rem;
    background:linear-gradient(180deg,#0d0d0d 0%,#080808 100%);
    text-align:center;
  }
  .check{
    width:56px;height:56px;margin:0 auto 1.25rem;
    border:1px solid #1f7a4d;border-radius:50%;
    display:flex;align-items:center;justify-content:center;
    color:#3ddc97;font-size:28px;line-height:1;
  }
  h1{
    font-size:1.15rem;font-weight:500;letter-spacing:.02em;
    margin:0 0 .5rem;color:#fafafa;
  }
  p{margin:.4rem 0;color:#9a9a9a;font-size:.92rem;line-height:1.55}
  .brand{color:#3ddc97}
  .hint{margin-top:1.5rem;color:#5a5a5a;font-size:.78rem}
</style></head>
<body>
  <main class="card">
    <div class="check" aria-hidden="true">&#10003;</div>
    <h1>Signed in</h1>
    <p><span class="brand">yottacode</span> is now authenticated with the OpenAI auth provider.</p>
    <p>You can close this tab and return to the terminal.</p>
    <p class="hint">~/.yottacode/auth/openai-auth.json</p>
  </main>
</body></html>`

// htmlError is the failure counterpart — same black-card aesthetic,
// red accents instead of green, with the OAuth error verbatim so the
// user has something to copy/paste when reporting issues.
const htmlError = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>yottacode — sign-in failed</title>
<style>
  *{box-sizing:border-box}
  html,body{height:100%%;margin:0}
  body{
    background:#0a0a0a;
    color:#e8e8e8;
    font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;
    padding:2rem;
  }
  .card{
    max-width:520px;width:100%%;
    border:1px solid #1f1f1f;border-radius:10px;
    padding:2.5rem 2rem;
    background:linear-gradient(180deg,#0d0d0d 0%%,#080808 100%%);
    text-align:center;
  }
  .x{
    width:56px;height:56px;margin:0 auto 1.25rem;
    border:1px solid #7a1f1f;border-radius:50%%;
    display:flex;align-items:center;justify-content:center;
    color:#ff6b6b;font-size:28px;line-height:1;
  }
  h1{
    font-size:1.15rem;font-weight:500;letter-spacing:.02em;
    margin:0 0 .75rem;color:#fafafa;
  }
  p{margin:.4rem 0;color:#9a9a9a;font-size:.92rem;line-height:1.55}
  pre{
    text-align:left;background:#070707;border:1px solid #1f1f1f;
    color:#d8d8d8;padding:.85rem 1rem;border-radius:6px;
    margin:1.25rem 0 .5rem;font-size:.82rem;
    white-space:pre-wrap;word-break:break-word;
  }
</style></head>
<body>
  <main class="card">
    <div class="x" aria-hidden="true">&#10005;</div>
    <h1>Sign-in failed</h1>
    <pre>%s</pre>
    <p>Return to the terminal for details.</p>
  </main>
</body></html>`
