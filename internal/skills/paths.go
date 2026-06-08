package skills

import (
	"fmt"
	"path/filepath"

	"github.com/yottadynamics/yottacode/internal/ychome"
)

// UserSkillsDir returns the global skills dir: $YOTTACODE_HOME/skills
// (when the env var is set) or ~/.yottacode/skills otherwise — the
// shared ychome.Dir resolution, so all global state lives under the
// same root regardless of override.
func UserSkillsDir() (string, error) {
	dir, err := ychome.Dir("skills")
	if err != nil {
		return "", fmt.Errorf("skills: %w", err)
	}
	return dir, nil
}

// ProjectSkillsDir returns the per-project skills dir:
// <cwd>/.yottacode/skills. Project-local definitions are checked in
// here so a team can ship a repo-specific skill alongside the
// codebase. Project wins on name collision with user and built-in.
func ProjectSkillsDir(cwd string) string {
	return filepath.Join(cwd, ".yottacode", "skills")
}
