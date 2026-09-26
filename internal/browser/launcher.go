package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var browserSearchPaths = map[string][]string{"darwin": {"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "/opt/homebrew/bin/chromium", "/usr/local/bin/chromium", "google-chrome", "chromium", "microsoft-edge"}, "linux": {"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium"}}

func findChromeBinary() (string, error) {
	c := browserSearchPaths[runtime.GOOS]
	if len(c) == 0 {
		return "", fmt.Errorf("%w: unsupported OS %q", ErrNoBinaryFound, runtime.GOOS)
	}
	for _, x := range c {
		if filepath.IsAbs(x) {
			if i, e := os.Stat(x); e == nil && !i.IsDir() {
				return x, nil
			}
		} else if p, e := exec.LookPath(x); e == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: searched PATH and %s", ErrNoBinaryFound, strings.Join(c, ", "))
}
func displayAvailable() bool { return displayAvailableFor(runtime.GOOS, os.Getenv) }
func displayAvailableFor(goos string, getenv func(string) string) bool {
	return goos == "darwin" || getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// shortTempRoot keeps Chrome's Unix-domain SingletonSocket path below its
// platform limit even when the process TMPDIR is nested under a long worktree.
func shortTempRoot() string {
	const root = "/tmp/yottacode-browser"
	if err := os.MkdirAll(root, 0o700); err == nil {
		_ = os.Chmod(root, 0o700)
		return root
	}
	return os.TempDir()
}

func newProfileDir() (string, error) {
	d, e := os.MkdirTemp(shortTempRoot(), "yottacode-browser-*")
	if e != nil {
		return "", fmt.Errorf("create isolated browser profile dir: %w", e)
	}
	return d, nil
}
