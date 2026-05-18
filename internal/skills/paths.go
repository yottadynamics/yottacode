package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UserSkillsDir returns the global skills dir: $YOTTACODE_HOME/skills
// (when the env var is set) or ~/.yottacode/skills otherwise. Mirrors
// the resolution subagents.UserAgentsDir uses so all global state
// lives under the same root regardless of override.
func UserSkillsDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("YOTTACODE_HOME")); home != "" {
		return filepath.Join(home, "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("skills: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "skills"), nil
}

// ProjectSkillsDir returns the per-project skills dir:
// <cwd>/.yottacode/skills. Project-local definitions are checked in
// here so a team can ship a repo-specific skill alongside the
// codebase. Project wins on name collision with user and built-in.
func ProjectSkillsDir(cwd string) string {
	return filepath.Join(cwd, ".yottacode", "skills")
}
