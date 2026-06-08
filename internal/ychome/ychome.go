// Package ychome resolves the root directory for yottacode's global
// state: $YOTTACODE_HOME when the override is set, ~/.yottacode
// otherwise. Every feature dir that follows the override (skills,
// plans, agent definitions, the memory tree) resolves through Dir so
// the rule lives in exactly one place; state that deliberately ignores
// the override (sessions, auth, checkpoints, config.toml) builds its
// path from os.UserHomeDir directly.
package ychome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir returns <root>/<subdir>, where root is $YOTTACODE_HOME (when
// set, after TrimSpace) or ~/.yottacode. The directory is not created;
// callers MkdirAll lazily where needed.
func Dir(subdir string) (string, error) {
	if home := strings.TrimSpace(os.Getenv("YOTTACODE_HOME")); home != "" {
		return filepath.Join(home, subdir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", subdir), nil
}
