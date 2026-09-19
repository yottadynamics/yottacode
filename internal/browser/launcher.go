package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-rod/rod/lib/launcher"
)

// browserSearchPaths mirrors the "detect, don't download" convention
// read_document uses for pdftotext/pandoc: well-known install locations
// plus PATH names, checked in order, first match wins. yottacode only
// ships linux and darwin builds (see CLAUDE.md), so those are the only
// two GOOS keys.
//
// darwin covers three real-world install paths, in order of likelihood:
// the GUI .app bundle (how most users get Chrome/Chromium/Edge — via the
// browser's own installer or a `brew install --cask`), Homebrew's CLI
// `chromium` formula (`brew install chromium`, distinct from the cask —
// common on developer machines) under both its Apple Silicon and Intel
// default prefixes, and finally a plain PATH lookup as a catch-all for
// anything installed or symlinked some other way. The two `/usr/bin/...`
// entries this list carried before were Linux install locations that
// never exist on macOS — dead weight, not a real fallback.
var browserSearchPaths = map[string][]string{
	"darwin": {
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/opt/homebrew/bin/chromium",
		"/usr/local/bin/chromium",
		"google-chrome",
		"chromium",
		"microsoft-edge",
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

// displayAvailable reports whether a headed browser window has anywhere to
// appear: macOS always has a window server for a logged-in user, while
// Linux needs an X11 or Wayland display in the environment. Chrome launched
// headed without one fails to start rather than degrading, so Handoff
// checks this up front instead of waiting out a launch timeout.
func displayAvailable() bool {
	return displayAvailableFor(runtime.GOOS, os.Getenv)
}

func displayAvailableFor(goos string, getenv func(string) string) bool {
	if goos == "darwin" {
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// hardenBrowserFlags undoes rod's launch defaults that weaken the browser's
// process isolation. rod ships them for its own convenience (a workaround for
// its cross-process iframe handling), but this browser is pointed at arbitrary
// sites by an agent, so it should keep Chrome's normal containment:
//
//   - site isolation is turned back on (rod passes
//     --disable-features=site-per-process and --disable-site-isolation-trials),
//     so a compromised renderer for one site can't read another site's data;
//   - the network service runs out of the browser process again (rod passes
//     --enable-features=NetworkServiceInProcess).
//
// Everything else rod sets (automation flags, background-throttling, popup
// handling) is left alone: those are what make it drivable, not what make it
// less safe. Chrome's own sandbox is untouched — nothing passes --no-sandbox.
func hardenBrowserFlags(l *launcher.Launcher) *launcher.Launcher {
	l.Set("disable-features", "TranslateUI")
	l.Delete("disable-site-isolation-trials")
	l.Delete("enable-features")
	return l
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
