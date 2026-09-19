package browser

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Provider names the backend a Manager drives.
const (
	// ProviderLocal launches a system Chrome/Chromium as a child process.
	// The default, and the only one that never sends page content off-machine.
	ProviderLocal = "local"
	// ProviderBrowserbase drives a cloud browser rented from Browserbase
	// over CDP. Opt-in: page content is processed by a third party.
	ProviderBrowserbase = "browserbase"
)

// Options configures a Manager beyond its defaults. The zero value is the
// plain, local, headless, isolated-profile browser NewManager gives.
type Options struct {
	// Provider selects the backend: "" or ProviderLocal, or
	// ProviderBrowserbase.
	Provider string

	// Stealth (local provider only) launches Chrome without the flags that
	// announce automation and gives it a user agent, client hints and
	// language list consistent with the real browser build instead of rod's
	// fake Mac laptop. It injects no JavaScript. It hides the obvious tells
	// and leaves the rest honest (software-rendered WebGL, a small screen);
	// it does not defeat serious bot detection, and it never solves a
	// challenge.
	Stealth bool

	// ProxyURL (local provider only) routes the browser's traffic through a
	// proxy: http://, https:// or socks5://, optionally with user:pass@ for
	// http(s) proxies. Chrome bypasses it for localhost, so dev servers keep
	// working.
	ProxyURL string

	// IdleTimeout closes a browser left unused that long. Zero means the
	// default (defaultIdleTimeout); negative disables reaping.
	IdleTimeout time.Duration

	// Browserbase configures ProviderBrowserbase.
	Browserbase BrowserbaseOptions

	// Camofox configures ProviderCamofox.
	Camofox CamofoxOptions
}

// ErrProviderNotConfigured means the selected provider is missing something
// it needs (credentials, a project id) — reported when the browser would
// launch, not when the Manager is built, so an unconfigured provider never
// blocks a session that doesn't use the browser.
var ErrProviderNotConfigured = errors.New("browser provider is not configured")

// ErrUnsupportedRemote means an action needs the browser to share a
// filesystem with yottacode, which a cloud browser does not.
var ErrUnsupportedRemote = errors.New("not supported on a remote browser session")

// proxySpec is a parsed Options.ProxyURL.
type proxySpec struct {
	// server is what goes to Chrome's --proxy-server: scheme://host[:port],
	// never with credentials (Chrome ignores them there).
	server string
	// user/pass answer a proxy's 407 challenge over CDP. Empty when the URL
	// carries no credentials.
	user, pass string
}

// parseProxy validates a proxy URL. Chrome cannot authenticate to a SOCKS
// proxy, so socks5 with credentials is rejected up front rather than
// failing every request later.
func parseProxy(raw string) (*proxySpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("browser proxy_url: %w", redactProxyErr(err))
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("browser proxy_url: scheme %q not supported (use http, https or socks5)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("browser proxy_url: missing host")
	}
	spec := &proxySpec{server: u.Scheme + "://" + u.Host}
	if u.User != nil {
		if u.Scheme == "socks5" {
			return nil, errors.New("browser proxy_url: Chrome cannot authenticate to a SOCKS proxy; use an http(s) proxy for credentials")
		}
		spec.user = u.User.Username()
		spec.pass, _ = u.User.Password()
	}
	return spec, nil
}

// redactProxyErr strips the URL from a parse error: net/url echoes the raw
// input, which may hold proxy credentials.
func redactProxyErr(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}

// resolveIdleTimeout applies Options.IdleTimeout's zero/negative rules.
func resolveIdleTimeout(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return defaultIdleTimeout
	case d < 0:
		return 0
	}
	return d
}

// NewManagerWithOptions is NewManager with the given options. It validates
// what it can without side effects (the proxy URL, the provider name) and
// launches nothing; provider credentials are checked when the browser first
// launches.
func NewManagerWithOptions(opts Options) (*Manager, error) {
	m := NewManager()
	m.idleTimeout = resolveIdleTimeout(opts.IdleTimeout)

	switch opts.Provider {
	case "", ProviderLocal:
		proxy, err := parseProxy(opts.ProxyURL)
		if err != nil {
			return nil, err
		}
		lo := launchOptions{stealth: opts.Stealth, proxy: proxy}
		m.newSession = func(ctx context.Context, bin, profileDir string) (pageSession, error) {
			return launchSession(ctx, bin, profileDir, lo)
		}
		m.provider = ProviderLocal
	case ProviderBrowserbase:
		if opts.Stealth || strings.TrimSpace(opts.ProxyURL) != "" {
			return nil, errors.New("browser: stealth and proxy_url apply to the local browser only; Browserbase brings its own")
		}
		if err := validateSecretBearingURL("browserbase base_url", opts.Browserbase.BaseURL); err != nil {
			return nil, err
		}
		client := newBrowserbaseClient(opts.Browserbase)
		m.newRemote = func(ctx context.Context) (pageSession, error) {
			return launchBrowserbaseSession(ctx, client)
		}
		m.provider = ProviderBrowserbase
	case ProviderCamofox:
		if opts.Stealth || strings.TrimSpace(opts.ProxyURL) != "" {
			return nil, errors.New("browser: stealth and proxy_url apply to the local browser only; Camofox does its own fingerprinting")
		}
		client := newCamofoxClient(opts.Camofox)
		m.newRemote = func(ctx context.Context) (pageSession, error) {
			return launchCamofoxSession(ctx, client)
		}
		m.provider = ProviderCamofox
		m.endpoint = redactURLUserinfo(client.base)
	default:
		return nil, fmt.Errorf("browser: unknown provider %q (expected %q, %q or %q)", opts.Provider, ProviderLocal, ProviderBrowserbase, ProviderCamofox)
	}
	return m, nil
}

// validateSecretBearingURL requires an endpoint that will be sent an API key
// to be https, or plain http only to this machine (a local gateway or a test
// double). An http:// URL to anywhere else would put the key on the wire in
// clear text. An empty value is fine: the caller falls back to its https
// default. The value is never echoed — a URL can carry credentials.
func validateSecretBearingURL(what, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("browser: %s is not a valid URL", what)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("browser: %s must be https:// (http:// is allowed only to localhost), since it is sent an API key", what)
}

// isLoopbackHost reports whether host names this machine.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.IsLoopback()
	}
	return false
}

// redactURLUserinfo drops any user:password from a URL so it can be shown.
func redactURLUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
