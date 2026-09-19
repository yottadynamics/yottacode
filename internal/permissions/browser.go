package permissions

import (
	"net/url"
	"regexp"
	"strings"
)

// Browser(<descriptor>) rules cover the approval-gated browser_* tools.
//
// The descriptor is the tool's verb (the tool name minus "browser_"), e.g.
// `Browser(inspect)` or `Browser(click)`. Verb-level rather than per-call
// because the call's arguments (a CSS selector) change every time, so a
// selector-level rule could never be reused. browser_navigate is the one
// exception: its descriptor carries the destination host
// (`Browser(navigate streeteasy.com)`), so "allow navigation" is always a
// per-site grant, never a blanket one.
//
// Only tools that prompt today are mapped. browser_status, browser_wait,
// browser_close, browser_tabs, browser_switch_tab and browser_close_tab never
// prompt and only report state or reduce capability, so they stay outside the
// permission layer — a `Browser(*)` deny must not be able to stop the user
// closing the browser.
var browserVerbs = map[string]struct{}{
	"navigate": {}, "screenshot": {}, "inspect": {}, "click": {}, "type": {},
	"hotkey": {}, "scroll": {}, "handoff": {}, "upload": {}, "download": {},
	"console_logs": {}, "network_requests": {},
}

// browserNavigateNonWeb prefixes the descriptor of a navigation that isn't a
// plain, unambiguous http(s) URL to a well-formed host. It is deliberately not
// spelled "navigate " so a `Browser(navigate *)` allow can never match it: a
// blanket "any site" rule must not silently cover file://, data:, or a URL
// the parser and Chrome might disagree about.
const browserNavigateNonWeb = "navigate-nonweb"

// browserHostRE is the conservative shape of a hostname we are willing to put
// in a rule: lowercase letters, digits, dots, hyphens, underscores, and colons
// (IPv6). Anything else — `*`/`?` (glob characters that would widen the rule),
// non-ASCII (IDN forms Chrome normalizes differently), whitespace — is not
// derivable and falls back to prompting.
var browserHostRE = regexp.MustCompile(`^[a-z0-9._:-]+$`)

func browserTarget(toolName, argsJSON string) Target {
	verb := strings.TrimPrefix(toolName, "browser_")
	if _, ok := browserVerbs[verb]; !ok {
		return Target{}
	}
	if verb == "navigate" {
		return Target{PermName: "Browser", Descriptor: browserNavigateDescriptor(extractField(argsJSON, "url"))}
	}
	return Target{PermName: "Browser", Descriptor: verb}
}

// browserNavigateDescriptor returns "navigate <host>" for a URL we can read
// unambiguously, and "navigate-nonweb <raw>" otherwise. "Unambiguously" is
// stricter than url.Parse: Go and Chrome split a URL's authority differently
// in a few cases (Chrome treats a backslash like a slash; userinfo and
// percent-escapes change which part is the host), and a host that Go reads as
// allowed-site.com while Chrome navigates elsewhere would let an allow rule
// cover the wrong site. Anything odd is treated as non-web, which only ever
// means "prompt".
func browserNavigateDescriptor(raw string) string {
	raw = strings.TrimSpace(raw)
	if host, ok := browserWebHost(raw); ok {
		return "navigate " + host
	}
	return browserNavigateNonWeb + " " + raw
}

// browserWebHost extracts the host of a plain http(s) URL, or reports false.
// The authority is cut the way Chrome cuts it (at the first of / \ ? #), so a
// space or backslash later in the path or query — common in model-written
// URLs like ".../search?q=long island" — doesn't matter, but anything odd in
// the authority itself does. Go's own parse must then agree on the host.
// BrowserNavigateHost returns the lowercase host of a plain, unambiguous
// http(s) URL, or ok=false for anything else (another scheme, userinfo, a
// backslash or glob character in the authority, non-ASCII, …). It is the same
// strict reading the Browser(navigate <host>) rules use, exported so other
// policy code (the /auto safety floor) judges a URL exactly as the permission
// rules do instead of re-parsing it its own way.
func BrowserNavigateHost(rawURL string) (host string, ok bool) {
	return browserWebHost(strings.TrimSpace(rawURL))
}

func browserWebHost(raw string) (string, bool) {
	scheme, rest, found := strings.Cut(raw, "://")
	if !found {
		return "", false
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
	default:
		return "", false
	}
	authority := rest
	if i := strings.IndexAny(rest, "/\\?#"); i >= 0 {
		authority = rest[:i]
	}
	if authority == "" || strings.ContainsAny(authority, "@% \t\r\n") {
		return "", false
	}
	host := authority
	if strings.HasPrefix(authority, "[") { // IPv6 literal, optional :port
		end := strings.Index(authority, "]")
		if end < 0 || !validPortSuffix(authority[end+1:]) {
			return "", false
		}
		host = authority[1:end]
	} else if i := strings.LastIndex(authority, ":"); i >= 0 {
		if !validPortSuffix(authority[i:]) {
			return "", false
		}
		host = authority[:i]
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || !browserHostRE.MatchString(host) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") != host {
		return "", false
	}
	return host, true
}

// validPortSuffix reports whether s is empty or ":" followed by digits only.
func validPortSuffix(s string) bool {
	if s == "" {
		return true
	}
	if s[0] != ':' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// deriveBrowserAllow builds the [S]/[A] rule for a browser call, or refuses.
//
//   - upload/download are never derived: they move local file bytes across the
//     write-path trust boundary (see docs/security-and-allow-lists.md), so
//     each stays a per-call decision — same posture as Fetch/Rollback above.
//   - navigate is derived only for a plain web host, and only for that exact
//     host (no subdomain wildcard). Hand-write `Browser(navigate *.example.com)`
//     if you want subdomains.
//   - Everything else is `Browser(<verb>)`.
func deriveBrowserAllow(descriptor string) (string, bool) {
	verb, rest, _ := strings.Cut(descriptor, " ")
	switch verb {
	case "upload", "download":
		return "", false
	case "navigate":
		if rest == "" {
			return "", false
		}
		return "Browser(navigate " + rest + ")", true
	}
	if _, ok := browserVerbs[verb]; ok && rest == "" {
		return "Browser(" + verb + ")", true
	}
	return "", false
}

// deriveBrowserDeny builds the [D] rule. Unlike the allow side it is offered
// for every browser tool (blocking upload/download is safe and useful) and for
// non-web navigations, where it blocks the whole non-web class.
func deriveBrowserDeny(descriptor string) (string, bool) {
	verb, rest, _ := strings.Cut(descriptor, " ")
	switch verb {
	case "navigate":
		if rest == "" {
			return "", false
		}
		return "Browser(navigate " + rest + ")", true
	case browserNavigateNonWeb:
		return "Browser(" + browserNavigateNonWeb + " *)", true
	}
	if _, ok := browserVerbs[verb]; ok && rest == "" {
		return "Browser(" + verb + ")", true
	}
	return "", false
}
