package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// resolvePath turns a model-supplied path into an absolute, cleaned
// filesystem path. It expands a leading "~/" or bare "~" to the user's
// home dir, then joins relative paths to cwd. This is the single
// normalization step every mutating fs tool runs before
// ValidateWritePath, so paths like "~/foo" can never end up creating a
// literal "~" directory under cwd.
func resolvePath(cwd, p string) string {
	if p == "" {
		return p
	}
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// expandHome rewrites a leading "~/" or bare "~" to the user's home
// directory. The "~user" form is left untouched.
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
