package browser

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestFindChromeBinary_Found(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "totally-fake-browser")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	orig := browserSearchPaths
	browserSearchPaths = map[string][]string{runtime.GOOS: {fake}}
	defer func() { browserSearchPaths = orig }()

	got, err := findChromeBinary()
	if err != nil {
		t.Fatalf("findChromeBinary: %v", err)
	}
	if got != fake {
		t.Errorf("got %q, want %q", got, fake)
	}
}

func TestFindChromeBinary_NotFound(t *testing.T) {
	orig := browserSearchPaths
	browserSearchPaths = map[string][]string{runtime.GOOS: {"/definitely/not/a/real/path/chrome-xyz-test"}}
	defer func() { browserSearchPaths = orig }()

	_, err := findChromeBinary()
	if !errors.Is(err, ErrNoBinaryFound) {
		t.Fatalf("got %v, want ErrNoBinaryFound", err)
	}
	if !strings.Contains(err.Error(), "chrome-xyz-test") {
		t.Errorf("error should name the searched path, got %v", err)
	}
}

func TestFindChromeBinary_UnsupportedOS(t *testing.T) {
	orig := browserSearchPaths
	browserSearchPaths = map[string][]string{}
	defer func() { browserSearchPaths = orig }()

	if _, err := findChromeBinary(); !errors.Is(err, ErrNoBinaryFound) {
		t.Errorf("got %v, want ErrNoBinaryFound", err)
	}
}

// TestBrowserSearchPaths_DarwinCoversRealInstalls is a regression test
// for a real gap found in review: the darwin list used to carry
// /usr/bin/google-chrome and /usr/bin/chromium — Linux install
// locations that never exist on macOS — while having no PATH fallback
// or Homebrew CLI formula paths at all, unlike the linux list. Pins that
// the dead entries are gone and the real ones (an app bundle, both
// Homebrew prefixes, a PATH fallback) are present.
func TestBrowserSearchPaths_DarwinCoversRealInstalls(t *testing.T) {
	darwin := browserSearchPaths["darwin"]
	for _, dead := range []string{"/usr/bin/google-chrome", "/usr/bin/chromium"} {
		if slices.Contains(darwin, dead) {
			t.Errorf("darwin search list still contains dead Linux path %q", dead)
		}
	}
	for _, want := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/opt/homebrew/bin/chromium",
		"/usr/local/bin/chromium",
		"chromium",
	} {
		if !slices.Contains(darwin, want) {
			t.Errorf("darwin search list missing %q: %v", want, darwin)
		}
	}
}

func TestNewProfileDir_IsolatedAndRemovable(t *testing.T) {
	dir, err := newProfileDir()
	if err != nil {
		t.Fatalf("newProfileDir: %v", err)
	}
	defer os.RemoveAll(dir)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("profile dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("profile path is not a directory: %s", dir)
	}
	if !strings.Contains(dir, "yottacode-browser-") {
		t.Errorf("profile dir %q doesn't look isolated/labeled", dir)
	}

	dir2, err := newProfileDir()
	if err != nil {
		t.Fatalf("newProfileDir (second): %v", err)
	}
	defer os.RemoveAll(dir2)
	if dir == dir2 {
		t.Error("two calls returned the same profile dir")
	}
}

func TestDisplayAvailableFor(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{"darwin always has a window server", "darwin", nil, true},
		{"linux with X11", "linux", map[string]string{"DISPLAY": ":0"}, true},
		{"linux with Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"linux over ssh with no display", "linux", map[string]string{"SSH_CONNECTION": "1 2 3 4"}, false},
		{"linux empty display vars", "linux", map[string]string{"DISPLAY": "", "WAYLAND_DISPLAY": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayAvailableFor(tc.goos, env(tc.env)); got != tc.want {
				t.Errorf("displayAvailableFor(%q, %v) = %t, want %t", tc.goos, tc.env, got, tc.want)
			}
		})
	}
}
