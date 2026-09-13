// Package execguard hardens an already-built *exec.Cmd against the
// orphaned-descendant hang: a subprocess we invoke (git, gh, ...) can
// itself fork a child that inherits its stdout/stderr pipes — a git
// commit launching $EDITOR, a GPG pinentry prompt, an SSH host-key or
// passphrase prompt, a credential helper. If ctx is canceled while
// that grandchild is still running, exec.CommandContext's default
// Cancel (Process.Kill, one PID) kills only the direct child; the
// grandchild survives as an orphan and can keep holding the pipe's
// write end open. Cmd.Wait() then blocks until every holder of that
// pipe closes it — which is not itself gated on ctx, so the caller
// hangs indefinitely, immune to Ctrl+C, regardless of how promptly ctx
// was canceled.
//
// Harden puts cmd in its own process group, replaces its Cancel with
// one that signals the whole group, and bounds the post-cancel wait
// with WaitDelay so an unrelated surviving descendant can never hang
// the call forever.
//
// internal/agent carries its own copy of this exact pattern
// (hardenGitCmd / procgroup_unix.go) predating this package — left
// alone rather than migrated, since it's already working and tested.
// This package exists for the other call sites (internal/github,
// internal/tui) that need the identical protection but cannot import
// internal/agent.
package execguard

import (
	"os"
	"os/exec"
	"time"
)

// DefaultKillTimeout bounds how long a canceled command's Cmd.Wait can
// take once its process group has been signaled. Matches the
// equivalent constants in internal/agent (hostExecKillTimeout) and
// internal/sandbox (execKillTimeout) — same reasoning, no extra grace
// period needed on top of it.
const DefaultKillTimeout = 5 * time.Second

// Harden applies the process-group + Cancel + WaitDelay protection to
// cmd. Call it after constructing the *exec.Cmd (Dir, Stdin, etc.) and
// before Run/Start — Cancel and SysProcAttr have no effect once the
// process has already started.
func Harden(cmd *exec.Cmd, killTimeout time.Duration) {
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = killTimeout
}

// HardenGit is Harden plus the git-specific environment that stops an
// interactive prompt from ever being launched in the first place:
// GIT_EDITOR / GIT_SEQUENCE_EDITOR = true make git treat any commit
// message or interactive-rebase todo list as already accepted
// verbatim; GIT_TERMINAL_PROMPT=0 makes a missing credential fail fast
// instead of trying to prompt on a terminal that was never there. This
// mirrors internal/agent's hardenGitCmd for callers outside that
// package. It does not (and cannot) suppress ssh's own prompts — the
// Harden half of this is what bounds those.
func HardenGit(cmd *exec.Cmd, killTimeout time.Duration) {
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_TERMINAL_PROMPT=0",
	)
	Harden(cmd, killTimeout)
}
