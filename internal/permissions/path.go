package permissions

import (
	"os"
	"path/filepath"
	"strings"
)

// expandHome rewrites a leading "~/" or bare "~" in p to the user's home
// directory. The "~user" form is left untouched — we don't resolve
// arbitrary user homes. Returns p unchanged when the home dir is
// unavailable.
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
