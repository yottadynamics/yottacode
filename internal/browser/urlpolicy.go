package browser

import (
	"fmt"
	"net/url"
	"strings"
)

// checkNavigableURL decides which URLs the agent may point the browser at
// directly (browser_navigate, and browser_download's url form). Only http,
// https, and about:blank are allowed; everything else is refused before a
// browser is even launched.
//
// The threat is the local machine, not the network. A file:// URL renders
// any file the user can read — including the credential files every read tool
// refuses to open (see agent.DefaultDenyReadPaths) — and browser_inspect /
// browser_screenshot then hand the contents to the model, so the browser
// would be a way around the read deny list. chrome://, view-source: and
// devtools: expose browser internals, and a javascript: URL would give the
// model a run-arbitrary-script primitive in whatever page is open, which no
// browser tool is meant to offer. An allowlist is used rather than a
// blocklist so a scheme nobody thought of (filesystem:, chrome-untrusted:,
// ...) is refused by default.
//
// This only guards URLs the agent supplies. Chrome itself already refuses a
// web page's own navigation to file:// (see
// TestIntegration_WebPageCannotNavigateToFile), so links, redirects and
// window.open from a loaded page can't reach these schemes either.
//
// Parsing is Go's, deliberately strict: it rejects control characters
// (Chrome silently strips tabs and newlines inside a URL, which is how
// "java\tscript:" tricks work), and anything it can't read as scheme "http" or
// "https" is refused rather than guessed at.
func checkNavigableURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "about:blank" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("%w: %q is not a valid URL: %v", ErrBlockedURL, clip(s), err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Hostname() == "" {
			return fmt.Errorf("%w: %q has no host", ErrBlockedURL, clip(s))
		}
		return nil
	case "":
		return fmt.Errorf("%w: %q has no scheme; use a full http:// or https:// URL", ErrBlockedURL, clip(s))
	}
	return fmt.Errorf("%w: %q is not allowed — only http, https, and about:blank URLs can be opened. To view a local file, serve it from a local web server and open its http://localhost URL", ErrBlockedURL, clip(s))
}

// clip bounds a URL echoed into an error: a data: URL can be megabytes.
func clip(s string) string {
	const max = 100
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
