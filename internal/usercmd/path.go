// Package usercmd loads user-authored slash commands from markdown
// files dropped in ~/.yottacode/commands/ (user scope) and
// <cwd>/.yottacode/commands/ (project scope).
//
// Layout mirrors Claude Code's custom-commands feature: filename
// becomes the command name, subdirectories become a colon-separated
// namespace (commands/frontend/component.md → /frontend:component),
// the body is sent to the agent as the user message for that turn
// (with $ARGUMENTS / $1..$9 substituted and @<path> file refs honored
// by the same pipeline that handles user-typed @-refs).
package usercmd

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/yottadynamics/yottacode/internal/ychome"
)

// nameSegmentPattern restricts each path segment (between subdirectory
// separators) to a Claude-Code-style identifier: lowercase letters,
// digits, hyphens, must start with a letter or digit. The full
// command name is segments joined by ":", e.g. "frontend:component".
//
// Mirrors internal/memory's topicNamePattern but drops the 64-char
// clamp so namespaced names like "team:frontend:component" remain
// expressible.
var nameSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// UserCommandsDir returns ~/.yottacode/commands/ — the cross-project
// custom-command directory. Commands here apply to every yottacode
// session for this user.
func UserCommandsDir() (string, error) {
	dir, err := ychome.Dir("commands")
	if err != nil {
		return "", fmt.Errorf("usercmd: %w", err)
	}
	return dir, nil
}

// ProjectCommandsDir returns <cwd>/.yottacode/commands/ — the per-repo
// custom-command directory. Commands here are committable so teams can
// share project-specific commands via git. Empty cwd returns "" so
// callers can detect "no project scope" without an error path.
func ProjectCommandsDir(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, ".yottacode", "commands")
}
