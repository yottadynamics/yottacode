package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// browserSearchPaths mirrors the "detect, don't download" convention
// read_document uses for pdftotext/pandoc: well-known install locations
// plus PATH names, checked in order, first match wins. yottacode only
// ships linux and darwin builds (see CLAUDE.md), so those are the only
// two GOOS keys.
var browserSearchPaths = map[string][]string{
	"darwin": {
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
	},
	"linux": {
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	},
}

// findChromeBinary searches PATH and well-known install locations for a
// system Chrome/Chromium/Edge binary. rod's own launcher defaults to
// downloading its own pinned Chromium build the first time it can't find
// one — this function exists so callers can pass the discovered path
// explicitly via Launcher.Bin and bypass that download entirely, matching
// the "no silent runtime download" bar the rest of the tool surface holds
// to. Returns ErrNoBinaryFound, wrapped with the exact candidates tried,
// when nothing resolves.
func findChromeBinary() (string, error) {
	candidates := browserSearchPaths[runtime.GOOS]
	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: unsupported OS %q", ErrNoBinaryFound, runtime.GOOS)
	}
	for _, c := range candidates {
		if filepath.IsAbs(c) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: searched PATH and %s", ErrNoBinaryFound, strings.Join(candidates, ", "))
}

// newProfileDir creates a fresh, isolated temp profile directory for one
// browser session. This is never the user's real, logged-in Chrome
// profile — v1's safety model deliberately keeps cookies/history/saved
// logins off the table entirely rather than gating access behind a flag.
// Manager.Close removes it.
func newProfileDir() (string, error) {
	dir, err := os.MkdirTemp("", "yottacode-browser-*")
	if err != nil {
		return "", fmt.Errorf("create isolated browser profile dir: %w", err)
	}
	return dir, nil
}
