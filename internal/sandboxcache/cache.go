// Package sandboxcache centralizes host paths shared by sandbox startup and
// sandboxed tool command preparation.
package sandboxcache

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GoCacheHomeSubdir names the ~/.yottacode subdirectory bind-mounted into every
// sandbox container for persistent Go build and module caches. Keep callers on
// this helper instead of duplicating the literal path; sandbox startup and
// run_tests command preparation must agree exactly for the cache to work.
const GoCacheHomeSubdir = "sandbox-go-cache"

// GoHostCacheDir returns ~/.yottacode/sandbox-go-cache, the host directory
// bind-mounted at the same absolute path inside every sandbox container.
func GoHostCacheDir() (string, error) {
	return yottacodeCacheDir(GoCacheHomeSubdir)
}

// HostGoScratchDir returns ~/.yottacode/host-go/<workspace>, the host-side
// scratch directory used by unsandboxed Go test runs. Keeping HOME/XDG/TMPDIR
// here prevents Go telemetry and temp/cache files from appearing in the repo.
func HostGoScratchDir(workspace string) (string, error) {
	return yottacodeCacheDir("host-go", safePathName(workspace))
}

func yottacodeCacheDir(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve yottacode cache dir: %w", err)
	}
	return filepath.Join(append([]string{home, ".yottacode"}, parts...)...), nil
}

func safePathName(path string) string {
	name := strings.ToLower(filepath.Base(filepath.Clean(path)))
	name = safePathChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		return "workspace"
	}
	return name
}

var safePathChars = regexp.MustCompile(`[^a-z0-9._-]+`)
