package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckNavigableURL_Allowed(t *testing.T) {
	for _, u := range []string{
		"https://streeteasy.com/for-rent/long-island-city",
		"http://localhost:3000/login",
		"HTTPS://Example.COM/x",
		"http://[::1]:8080/",
		"http://127.0.0.1:9222/",
		"https://user@example.com/",
		"https://example.com/search?q=long island city", // raw space in the query is fine
		"  https://example.com/  ",
		"about:blank",
	} {
		if err := checkNavigableURL(u); err != nil {
			t.Errorf("checkNavigableURL(%q) = %v, want allowed", u, err)
		}
	}
}

// Every one of these gives the agent something a browser tool must not: local
// file contents (bypassing the read deny list), browser internals, or script
// execution in the open page.
func TestCheckNavigableURL_Blocked(t *testing.T) {
	cases := []struct{ name, url string }{
		{"file url", "file:///home/me/.ssh/id_rsa"},
		{"file url with host", "file://localhost/etc/passwd"},
		{"uppercase file", "FILE:///etc/passwd"},
		{"chrome internals", "chrome://version"},
		{"chrome settings", "chrome://settings/passwords"},
		{"chrome untrusted", "chrome-untrusted://terminal"},
		{"devtools", "devtools://devtools/bundled/inspector.html"},
		{"view-source", "view-source:https://example.com"},
		{"javascript", "javascript:alert(document.cookie)"},
		{"javascript uppercase", "JavaScript:alert(1)"},
		{"data url", "data:text/html,<script>alert(1)</script>"},
		{"blob", "blob:https://example.com/abc"},
		{"filesystem", "filesystem:https://example.com/temporary/x"},
		{"ftp", "ftp://example.com/x"},
		{"about other than blank", "about:srcdoc"},
		{"about blank with fragment", "about:blank#x"},
		{"no scheme", "example.com"},
		{"scheme-relative", "//example.com/x"},
		{"empty", ""},
		{"whitespace only", "   "},
		{"http with no host", "http:///path"},
		{"http opaque (no slashes)", "http:example.com"},
		// Chrome strips tabs/newlines inside a URL, so these would land on a
		// blocked scheme there; Go's parser rejects the control characters.
		{"tab inside scheme", "java\tscript:alert(1)"},
		{"newline inside scheme", "java\nscript:alert(1)"},
		{"leading control char", "\x01javascript:alert(1)"},
		{"tab inside file", "fi\tle:///etc/passwd"},
		{"nul byte", "https://exa\x00mple.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNavigableURL(tc.url)
			if !errors.Is(err, ErrBlockedURL) {
				t.Errorf("checkNavigableURL(%q) = %v, want ErrBlockedURL", tc.url, err)
			}
		})
	}
}

func TestCheckNavigableURL_ErrorIsBoundedAndHelpful(t *testing.T) {
	huge := "data:text/html," + strings.Repeat("A", 1<<20)
	err := checkNavigableURL(huge)
	if err == nil {
		t.Fatal("want an error for a data: URL")
	}
	if len(err.Error()) > 600 {
		t.Errorf("error echoes the whole URL (%d bytes); it must be clipped", len(err.Error()))
	}
	// The model reads this: it must say what to do instead.
	if !strings.Contains(err.Error(), "local web server") {
		t.Errorf("error %q should tell the caller how to view a local file", err)
	}
}

// A blocked URL must never launch a browser or touch the lock: there is nothing
// to clean up, and no process started on behalf of a refused request.
func TestManager_NavigateBlockedURLNeverLaunches(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("launched a browser for a blocked URL")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("looked for a browser for a blocked URL"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("made a profile dir for a blocked URL"); return "", nil },
	}
	for _, u := range []string{"file:///etc/passwd", "chrome://version", "javascript:alert(1)", "data:text/html,x"} {
		if _, err := m.Navigate(context.Background(), u, ""); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("Navigate(%q) = %v, want ErrBlockedURL", u, err)
		}
	}
}

func TestManager_DownloadBlockedURLNeverLaunches(t *testing.T) {
	m := &Manager{
		newSession: func(context.Context, string, string) (pageSession, error) {
			t.Fatal("launched a browser")
			return nil, nil
		},
		findBinary:   func() (string, error) { t.Fatal("looked for a browser"); return "", nil },
		mkProfileDir: func() (string, error) { t.Fatal("made a profile dir"); return "", nil },
	}
	if _, err := m.Download(context.Background(), "", "file:///home/me/.ssh/id_rsa", t.TempDir()+"/out"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("Download(file://) = %v, want ErrBlockedURL", err)
	}
}

// The selector form of Download has no URL of its own to check, and an allowed
// http URL still reaches the session.
func TestManager_NavigateAllowedURLStillWorks(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "https://example.com/", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if fake.callCount("navigate") != 1 {
		t.Errorf("an allowed URL should reach the session: %v", fake.calls)
	}
}
