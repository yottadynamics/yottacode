package permissions

import (
	"strings"
	"testing"
)

func TestBrowserTarget_MappedVerbs(t *testing.T) {
	// Exactly the browser_* tools that prompt today.
	cases := []struct {
		tool, args, want string
	}{
		{"browser_inspect", `{"selector":"main li:nth-of-type(1)"}`, "inspect"},
		{"browser_screenshot", `{}`, "screenshot"},
		{"browser_click", `{"selector":"#go"}`, "click"},
		{"browser_type", `{"selector":"#q","text":"hi"}`, "type"},
		{"browser_hotkey", `{"keys":"Enter"}`, "hotkey"},
		{"browser_scroll", `{"direction":"down"}`, "scroll"},
		{"browser_handoff", `{}`, "handoff"},
		{"browser_upload", `{"selector":"#f","paths":["a.txt"]}`, "upload"},
		{"browser_download", `{"selector":"#dl","path":"out.bin"}`, "download"},
		{"browser_console_logs", `{}`, "console_logs"},
		{"browser_network_requests", `{}`, "network_requests"},
		{"browser_navigate", `{"url":"https://streeteasy.com/for-rent/long-island-city"}`, "navigate streeteasy.com"},
	}
	for _, tc := range cases {
		got := targetFor(tc.tool, tc.args, "/repo")
		if got.PermName != "Browser" || got.Descriptor != tc.want || got.Multi || got.IsPath {
			t.Errorf("targetFor(%s) = %+v, want Browser(%q)", tc.tool, got, tc.want)
		}
	}
}

// The tools that never prompt must stay outside the permission layer: a
// Browser(*) deny must not be able to stop the user closing the browser.
func TestBrowserTarget_NoTargetForToolsThatNeverPrompt(t *testing.T) {
	for _, tool := range []string{
		"browser_status", "browser_wait", "browser_close",
		"browser_tabs", "browser_switch_tab", "browser_close_tab",
		"browser_does_not_exist",
	} {
		if got := targetFor(tool, `{}`, "/repo"); got.PermName != "" {
			t.Errorf("targetFor(%s).PermName = %q, want none", tool, got.PermName)
		}
	}
}

func TestBrowserNavigateDescriptor(t *testing.T) {
	const nonWeb = browserNavigateNonWeb + " "
	cases := []struct {
		name, url, want string
	}{
		{"plain https", "https://streeteasy.com/for-rent/long-island-city", "navigate streeteasy.com"},
		{"host is lowercased, trailing dot dropped", "HTTPS://StreetEasy.COM./x", "navigate streeteasy.com"},
		{"port is not part of the host", "http://localhost:3000/login", "navigate localhost"},
		{"ipv6 literal", "http://[::1]:8080/", "navigate ::1"},
		{"surrounding whitespace trimmed", "  https://example.com/  ", "navigate example.com"},
		{"query and fragment ignored", "https://example.com?q=1#frag", "navigate example.com"},
		// Model-written URLs often carry a raw space in the query; that must not
		// demote an ordinary site to "prompt every time".
		{"space in query is fine", "https://example.com/search?q=long island city", "navigate example.com"},
		{"backslash in path is fine", `https://example.com/a\b`, "navigate example.com"},

		{"file url", "file:///etc/passwd", nonWeb + "file:///etc/passwd"},
		{"data url", "data:text/html,<h1>x</h1>", nonWeb + "data:text/html,<h1>x</h1>"},
		{"javascript url", "javascript:alert(1)", nonWeb + "javascript:alert(1)"},
		{"about:blank", "about:blank", nonWeb + "about:blank"},
		{"no scheme", "example.com", nonWeb + "example.com"},
		{"empty", "", nonWeb},
		{"chrome scheme", "chrome://settings", nonWeb + "chrome://settings"},

		// Parser-differential and rule-widening shapes: all must degrade to
		// non-web (which only ever means "prompt"), never to an allow-able host.
		{"backslash ends authority in Chrome", `https://good.com\@evil.com/`, nonWeb + `https://good.com\@evil.com/`},
		{"userinfo", "https://user:pw@good.com/", nonWeb + "https://user:pw@good.com/"},
		{"userinfo host swap", "https://good.com@evil.com/", nonWeb + "https://good.com@evil.com/"},
		{"glob star subdomain", "https://*.evil.com/", nonWeb + "https://*.evil.com/"},
		{"bare glob star host", "https://*/", nonWeb + "https://*/"},
		// "?" ends the authority, so the host really is "exampl" (Chrome and Go
		// agree) and the derived rule has no glob character in it.
		{"question mark starts the query", "https://exampl?.com/", "navigate exampl"},
		{"percent-encoded host", "https://%67ood.com/", nonWeb + "https://%67ood.com/"},
		{"non-ascii host", "https://münchen.de/", nonWeb + "https://münchen.de/"},
		{"tab in authority", "https://good.com\t.evil.com/", nonWeb + "https://good.com\t.evil.com/"},
		{"non-numeric port", "https://good.com:80x/", nonWeb + "https://good.com:80x/"},
		{"empty host", "https:///path", nonWeb + "https:///path"},
		{"unterminated ipv6", "http://[::1/", nonWeb + "http://[::1/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := browserNavigateDescriptor(tc.url); got != tc.want {
				t.Errorf("browserNavigateDescriptor(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestDeriveAllowRule_Browser(t *testing.T) {
	cases := []struct {
		tool, args string
		want       string
		ok         bool
	}{
		{"browser_inspect", `{"selector":"main li"}`, "Browser(inspect)", true},
		{"browser_screenshot", `{}`, "Browser(screenshot)", true},
		{"browser_click", `{"selector":"#go"}`, "Browser(click)", true},
		{"browser_type", `{"selector":"#q","text":"x"}`, "Browser(type)", true},
		{"browser_hotkey", `{"keys":"Enter"}`, "Browser(hotkey)", true},
		{"browser_scroll", `{}`, "Browser(scroll)", true},
		{"browser_handoff", `{}`, "Browser(handoff)", true},
		{"browser_console_logs", `{}`, "Browser(console_logs)", true},
		{"browser_network_requests", `{}`, "Browser(network_requests)", true},
		// navigate is per exact site.
		{"browser_navigate", `{"url":"https://streeteasy.com/x"}`, "Browser(navigate streeteasy.com)", true},
		// File-boundary tools are never derivable for allow.
		{"browser_upload", `{"selector":"#f","paths":["a"]}`, "", false},
		{"browser_download", `{"selector":"#d","path":"o"}`, "", false},
		// Non-web navigation is never derivable for allow.
		{"browser_navigate", `{"url":"file:///etc/passwd"}`, "", false},
		{"browser_navigate", `{"url":"https://*.evil.com/"}`, "", false},
		// No approval, no rule.
		{"browser_close", `{}`, "", false},
		{"browser_status", `{}`, "", false},
	}
	for _, tc := range cases {
		got, ok := DeriveAllowRule(tc.tool, tc.args, "/repo", nil)
		if got != tc.want || ok != tc.ok {
			t.Errorf("DeriveAllowRule(%s, %s) = (%q, %t), want (%q, %t)", tc.tool, tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDeriveDenyRule_Browser(t *testing.T) {
	cases := []struct {
		tool, args string
		want       string
		ok         bool
	}{
		{"browser_inspect", `{}`, "Browser(inspect)", true},
		{"browser_click", `{"selector":"#x"}`, "Browser(click)", true},
		// Blocking is safe and useful for the file-boundary tools too.
		{"browser_upload", `{"selector":"#f","paths":["a"]}`, "Browser(upload)", true},
		{"browser_download", `{"selector":"#d","path":"o"}`, "Browser(download)", true},
		{"browser_navigate", `{"url":"https://evil.example/x"}`, "Browser(navigate evil.example)", true},
		{"browser_navigate", `{"url":"file:///etc/passwd"}`, "Browser(navigate-nonweb *)", true},
		{"browser_close", `{}`, "", false},
	}
	for _, tc := range cases {
		got, ok := DeriveDenyRule(tc.tool, tc.args, "/repo")
		if got != tc.want || ok != tc.ok {
			t.Errorf("DeriveDenyRule(%s, %s) = (%q, %t), want (%q, %t)", tc.tool, tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

// End to end through the same Evaluate the agent loop calls: what "allow for
// this session" actually grants, and what it must not.
func TestBrowserRules_SessionAllowScope(t *testing.T) {
	p := LoadEmpty(t.TempDir())
	inspect := `{"selector":"main li:nth-of-type(1)"}`
	if got := p.Evaluate("browser_inspect", inspect); got != Default {
		t.Fatalf("before any grant = %v, want Default (prompt)", got)
	}
	rule, ok := DeriveAllowRule("browser_inspect", inspect, "/repo", nil)
	if !ok {
		t.Fatal("no rule derived for browser_inspect")
	}
	if err := p.AddSessionAllow(rule); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}

	// Any later inspect, whatever its selector, is covered...
	if got := p.Evaluate("browser_inspect", `{"selector":"#totally-different"}`); got != Allow {
		t.Errorf("later inspect = %v, want Allow", got)
	}
	// ...but the grant is for that verb only.
	for _, tool := range []string{"browser_click", "browser_type", "browser_screenshot", "browser_upload", "browser_handoff"} {
		if got := p.Evaluate(tool, `{"selector":"#x","text":"t","paths":["a"]}`); got != Default {
			t.Errorf("%s after an inspect grant = %v, want Default", tool, got)
		}
	}
}

func TestBrowserRules_NavigateIsPerExactSite(t *testing.T) {
	p := LoadEmpty(t.TempDir())
	if err := p.AddSessionAllow("Browser(navigate streeteasy.com)"); err != nil {
		t.Fatal(err)
	}
	nav := func(u string) Decision {
		return p.Evaluate("browser_navigate", `{"url":"`+strings.ReplaceAll(u, `\`, `\\`)+`"}`)
	}

	for _, u := range []string{
		"https://streeteasy.com/for-rent/long-island-city",
		"http://STREETEASY.com:8443/x?y=1",
	} {
		if got := nav(u); got != Allow {
			t.Errorf("navigate %s = %v, want Allow", u, got)
		}
	}
	for _, u := range []string{
		"https://evil.com/",
		"https://streeteasy.com.evil.com/",   // suffix trick
		"https://evil.com/?u=streeteasy.com", // name only in the query
		"https://www.streeteasy.com/",        // subdomain: not granted by an exact-host rule
		`https://streeteasy.com\@evil.com/`,  // Go/Chrome authority differential
		"https://streeteasy.com@evil.com/",   // userinfo swap
		"file:///etc/passwd",
	} {
		if got := nav(u); got != Default {
			t.Errorf("navigate %s = %v, want Default (still prompts)", u, got)
		}
	}
}

// A hand-written blanket "any site" rule covers web hosts but never non-web
// schemes, so it can't be used to read local files through the browser.
func TestBrowserRules_NavigateWildcardDoesNotCoverNonWeb(t *testing.T) {
	p := LoadEmpty(t.TempDir())
	if err := p.AddSessionAllow("Browser(navigate *)"); err != nil {
		t.Fatal(err)
	}
	if got := p.Evaluate("browser_navigate", `{"url":"https://anything.example/"}`); got != Allow {
		t.Errorf("web navigate under wildcard = %v, want Allow", got)
	}
	for _, u := range []string{"file:///etc/passwd", "data:text/html,x", "javascript:alert(1)", "https://*.evil.com/"} {
		if got := p.Evaluate("browser_navigate", `{"url":"`+u+`"}`); got != Default {
			t.Errorf("non-web navigate %s under wildcard = %v, want Default", u, got)
		}
	}
}

func TestBrowserRules_DenyBeatsAllowAndSparesClose(t *testing.T) {
	p := LoadEmpty(t.TempDir())
	if err := p.AddSessionAllow("Browser(inspect)"); err != nil {
		t.Fatal(err)
	}
	if err := p.AddDeny("Browser(inspect)"); err != nil {
		t.Fatal(err)
	}
	if got := p.Evaluate("browser_inspect", `{}`); got != Deny {
		t.Errorf("inspect with allow+deny = %v, want Deny", got)
	}

	// A catch-all block covers the prompting tools...
	if err := p.AddDeny("Browser(*)"); err != nil {
		t.Fatal(err)
	}
	if got := p.Evaluate("browser_click", `{"selector":"#x"}`); got != Deny {
		t.Errorf("click under Browser(*) deny = %v, want Deny", got)
	}
	// ...but never the ones that only report state or reduce capability.
	for _, tool := range []string{"browser_close", "browser_status", "browser_wait", "browser_tabs", "browser_close_tab", "browser_switch_tab"} {
		if got := p.Evaluate(tool, `{}`); got != Default {
			t.Errorf("%s under Browser(*) deny = %v, want Default (must stay usable)", tool, got)
		}
	}
}

// "[A] always" persists to permissions.local.json and must survive a reload.
func TestBrowserRules_AlwaysAllowPersistsAcrossReload(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	rule, ok := DeriveAllowRule("browser_screenshot", `{}`, cwd, nil)
	if !ok {
		t.Fatal("no rule derived")
	}
	if err := p.AddAllow(rule); err != nil {
		t.Fatalf("AddAllow: %v", err)
	}
	reloaded, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.Evaluate("browser_screenshot", `{}`); got != Allow {
		t.Errorf("after reload = %v, want Allow", got)
	}
	if got := reloaded.Evaluate("browser_inspect", `{}`); got != Default {
		t.Errorf("a screenshot grant must not cover inspect, got %v", got)
	}
}

func TestBrowserRules_Lint(t *testing.T) {
	// LintWarnings inspects persisted rules (session-only grants aren't in
	// the files it audits), so these go through AddAllow.
	p := LoadEmpty(t.TempDir())
	for _, rule := range []string{"Browser(inspect)", "Browser(navigate streeteasy.com)"} {
		if err := p.AddAllow(rule); err != nil {
			t.Fatal(err)
		}
	}
	if w := p.LintWarnings(); len(w) != 0 {
		t.Errorf("narrow browser rules should lint clean (Browser is a known rule prefix), got %v", w)
	}

	for _, rule := range []string{"Browser(*)", "Browser(navigate *)"} {
		q := LoadEmpty(t.TempDir())
		if err := q.AddAllow(rule); err != nil {
			t.Fatal(err)
		}
		w := q.LintWarnings()
		if len(w) != 1 || !strings.Contains(w[0], rule) || strings.Contains(w[0], "unknown rule prefix") {
			t.Errorf("%s: want exactly one broad-rule warning, got %v", rule, w)
		}
	}
}

// BrowserNavigateHost is the strict reading other policy code (the /auto
// floor) relies on; it must agree with the Browser(navigate <host>) rules.
func TestBrowserNavigateHost_MatchesTheRuleDescriptor(t *testing.T) {
	cases := []struct {
		url, host string
		ok        bool
	}{
		{"https://StreetEasy.com./x", "streeteasy.com", true},
		{"  http://localhost:3000/  ", "localhost", true},
		{"http://[::1]:8080/", "::1", true},
		{"file:///etc/passwd", "", false},
		{"https://good.com@evil.com/", "", false},
		{"https://*.evil.com/", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		host, ok := BrowserNavigateHost(tc.url)
		if host != tc.host || ok != tc.ok {
			t.Errorf("BrowserNavigateHost(%q) = (%q, %t), want (%q, %t)", tc.url, host, ok, tc.host, tc.ok)
		}
		// Whatever the rules descriptor says must agree.
		want := browserNavigateDescriptor(tc.url)
		if ok && want != "navigate "+host {
			t.Errorf("BrowserNavigateHost(%q) host %q disagrees with descriptor %q", tc.url, host, want)
		}
		if !ok && strings.HasPrefix(want, "navigate ") {
			t.Errorf("BrowserNavigateHost(%q) refused, but the rules descriptor accepted it as %q", tc.url, want)
		}
	}
}
