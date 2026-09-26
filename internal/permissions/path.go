package permissions

import (
	"os"
	"path/filepath"
	"strings"
)

// expandHome rewrites a leading "~/" or bare "~" in p to the user's home
// directory. The "~user" form is left untouched — we don't resolve arbitrary
// user homes. Returns p unchanged when the home dir is unavailable.
func expandHome(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	if len(p) > 1 && p[1] != '/' && p[1] != filepath.Separator {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
}

// canonicalizePath resolves symlinks in the existing portion of a path and
// preserves a nonexistent suffix. This matters on macOS, where /var commonly
// resolves through /private/var, including for paths that will be created later.
func canonicalizePath(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	original := path
	var suffix []string
	for {
		if _, err := os.Lstat(path); err == nil {
			if real, err := filepath.EvalSymlinks(path); err == nil {
				path = real
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				path = filepath.Join(path, suffix[i])
			}
			return filepath.Clean(path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Clean(original)
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

// canonicalizePattern resolves only the concrete portion of an absolute
// permission pattern, preserving glob syntax in its unresolved suffix.
func canonicalizePattern(pattern string) string {
	pattern = expandHome(pattern)
	if !filepath.IsAbs(pattern) {
		return filepath.ToSlash(pattern)
	}
	wildcard := strings.IndexAny(pattern, "*?[")
	if wildcard < 0 {
		return filepath.ToSlash(canonicalizePath(pattern))
	}
	prefix, suffix := pattern[:wildcard], pattern[wildcard:]
	resolved := canonicalizePath(prefix)
	if strings.HasSuffix(prefix, string(filepath.Separator)) || strings.HasSuffix(prefix, "/") {
		resolved += string(filepath.Separator)
	}
	return filepath.ToSlash(resolved + suffix)
}
