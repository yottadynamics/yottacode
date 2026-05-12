// Package subagents owns typed-subagent configuration, the per-session
// task registry for background runs, and the discovery + parsing of
// agent definition files (`.yottacode/agents/*.md` and
// `~/.yottacode/agents/*.md`). The agent loop itself imports this
// package to look up `Agent` tool invocations; nothing here imports
// the agent package, which keeps the dependency direction clean.
package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// UserAgentsDir returns the global agents dir: $YOTTACODE_HOME/agents
// (when the env var is set) or ~/.yottacode/agents otherwise. Mirrors
// the resolution agent.PlansDir uses so all global state lives under
// the same root regardless of override.
func UserAgentsDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("YOTTACODE_HOME")); home != "" {
		return filepath.Join(home, "agents"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("subagents: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "agents"), nil
}

// ProjectAgentsDir returns the per-project agents dir: <cwd>/.yottacode/agents.
// Project-local definitions are checked in here so a team can ship a
// repo-specific `Explore`/`Plan` flavor alongside the codebase. Project
// wins on name collision with the user-scope dir.
func ProjectAgentsDir(cwd string) string {
	return filepath.Join(cwd, ".yottacode", "agents")
}

// TranscriptDirFor resolves where subagent run transcripts get
// persisted: ~/.yottacode/projects/<slug>/subagents/. The slug is the
// same one internal/memory uses for project-scoped memory, so all
// per-project agent state lives under one root.
func TranscriptDirFor(cwd string) (string, error) {
	if home := strings.TrimSpace(os.Getenv("YOTTACODE_HOME")); home != "" {
		return filepath.Join(home, "projects", memory.ProjectSlug(cwd), "subagents"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("subagents: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "projects", memory.ProjectSlug(cwd), "subagents"), nil
}

// EnsureTranscriptDir creates the transcript dir if missing. Returns the
// absolute path on success. The agent tool opens transcript files
// lazily; the dir creation happens up-front so the first write doesn't
// stall on MkdirAll inside a hot path.
func EnsureTranscriptDir(cwd string) (string, error) {
	dir, err := TranscriptDirFor(cwd)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("subagents: create %q: %w", dir, err)
	}
	return dir, nil
}
