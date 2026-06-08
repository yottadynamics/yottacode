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

	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/ychome"
)

// UserAgentsDir returns the global agents dir: $YOTTACODE_HOME/agents
// (when the env var is set) or ~/.yottacode/agents otherwise — the
// shared ychome.Dir resolution, so all global state lives under the
// same root regardless of override.
func UserAgentsDir() (string, error) {
	dir, err := ychome.Dir("agents")
	if err != nil {
		return "", fmt.Errorf("subagents: %w", err)
	}
	return dir, nil
}

// ProjectAgentsDir returns the per-project agents dir: <cwd>/.yottacode/agents.
// Project-local definitions are checked in here so a team can ship a
// repo-specific `Explore`/`Plan` flavor alongside the codebase. Project
// wins on name collision with the user-scope dir.
func ProjectAgentsDir(cwd string) string {
	return filepath.Join(cwd, ".yottacode", "agents")
}

// TranscriptDirFor resolves where subagent run transcripts get
// persisted: <project memory dir>/subagents/ — i.e.
// ~/.yottacode/memory/projects/<slug>/subagents/. Transcripts nest
// inside the project's memory dir so every per-project artifact is
// discoverable from one `ls ~/.yottacode/memory` tree; the memory
// loader skips subdirectories, so transcript .md files never load as
// memories. Root resolution (incl. the $YOTTACODE_HOME override) is
// owned by memory.ProjectMemoryDir.
func TranscriptDirFor(cwd string) (string, error) {
	dir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		return "", fmt.Errorf("subagents: resolve transcript dir: %w", err)
	}
	return filepath.Join(dir, memory.SubagentsDirName), nil
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
