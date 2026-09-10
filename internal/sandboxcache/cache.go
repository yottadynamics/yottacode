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

const GoCacheHomeSubdir = "sandbox-go-cache"

func GoHostCacheDir() (string, error) {
	return filepath.Join(string(filepath.Separator), "var", "tmp", fmt.Sprintf("yottacode-%d", os.Getuid()), GoCacheHomeSubdir), nil
}

// GoHostCacheDirForWorkspace returns the canonical sandbox cache mount. It is
// intentionally independent of both HOME and the particular checkout path:
// Podman is created for the repository root while commands may later run from
// a managed worktree, and both sides must still name the identical mount.
func GoHostCacheDirForWorkspace(_ string) (string, error) { return GoHostCacheDir() }

func HostGoScratchDir(workspace string) (string, error) {
	return yottacodeCacheDir(workspace, "host-go", safePathName(workspace))
}

func HostShellScratchDir(workspace string) (string, error) {
	return yottacodeCacheDir(workspace, "host-shell", safePathName(workspace))
}

func yottacodeCacheDir(workspace string, parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve yottacode cache dir: %w", err)
	}
	base := filepath.Join(home, ".yottacode")
	if workspace != "" && PathWithinWorkspace(base, workspace) {
		// /var/tmp is deliberately used instead of /tmp: sandbox test binaries
		// need executable, durable scratch and /tmp may be noexec/space-limited.
		base = filepath.Join(string(filepath.Separator), "var", "tmp", fmt.Sprintf("yottacode-%d", os.Getuid()))
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}

// PathWithinWorkspace reports whether path resolves to root or one of its
// descendants. It is shared by host command environment policy and cache
// placement so both make the same decision for an inherited workspace HOME.
func PathWithinWorkspace(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safePathName(path string) string {
	name := strings.ToLower(filepath.Base(filepath.Clean(path)))
	name = safePathChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		name = "workspace"
	}
	return name
}

var safePathChars = regexp.MustCompile(`[^a-z0-9._-]+`)
