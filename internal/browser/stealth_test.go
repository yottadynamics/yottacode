package browser

import (
	"runtime"
	"strings"
	"testing"
)

func TestStealthUserAgent(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		wantEmpty bool
	}{
		{"normal version", "153.0.7000.12", false},
		{"garbage", "not-a-version", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stealthUserAgent(tc.version)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("stealthUserAgent(%q) = %q, want empty", tc.version, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("stealthUserAgent(%q) = empty, want a user agent", tc.version)
			}
			if strings.Contains(got, "Headless") {
				t.Errorf("stealthUserAgent(%q) = %q, still announces Headless", tc.version, got)
			}
			if !strings.Contains(got, "Chrome/153.0.0.0") {
				t.Errorf("stealthUserAgent(%q) = %q, want it to carry the reduced version", tc.version, got)
			}
			wantOS := "X11; Linux x86_64"
			if runtime.GOOS == "darwin" {
				wantOS = "Macintosh; Intel Mac OS X 10_15_7"
			}
			if !strings.Contains(got, wantOS) {
				t.Errorf("stealthUserAgent(%q) = %q, want it to name this host's OS (%q)", tc.version, got, wantOS)
			}
		})
	}
}

func TestBuildStealthProfile_ReplacesHeadlessMarkers(t *testing.T) {
	sp := buildStealthProfile(
		"HeadlessChrome/153.0.7000.12",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/153.0.7000.12 Safari/537.36",
	)
	if strings.Contains(sp.userAgent, "Headless") {
		t.Errorf("user agent still announces headless: %q", sp.userAgent)
	}
	if sp.meta == nil {
		t.Fatal("want non-nil client hints metadata")
	}
	for _, b := range sp.meta.Brands {
		if strings.Contains(b.Version, "Headless") {
			t.Errorf("brand %q version still announces headless: %q", b.Brand, b.Version)
		}
	}
	for _, b := range sp.meta.FullVersionList {
		if strings.Contains(b.Version, "Headless") {
			t.Errorf("full-version brand %q still announces headless: %q", b.Brand, b.Version)
		}
	}
}

func TestBuildStealthProfile_FallsBackWhenProductHasNoVersion(t *testing.T) {
	sp := buildStealthProfile("HeadlessChrome", "Mozilla/5.0 HeadlessChrome Safari/537.36")
	if strings.Contains(sp.userAgent, "Headless") {
		t.Errorf("fallback user agent still announces headless: %q", sp.userAgent)
	}
	if sp.userAgent != "Mozilla/5.0 Chrome Safari/537.36" {
		t.Errorf("fallback user agent = %q, want the plain substitution", sp.userAgent)
	}
}
