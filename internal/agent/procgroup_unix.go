//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts cmd in its own process group so
// killProcessGroup can signal every descendant it spawns — not just the
// direct child exec.CommandContext's default Cancel would otherwise
// kill alone. Mirrors internal/lsp/process_unix.go's identical pattern.
//
// Why this matters here: a subprocess we invoke (git, /bin/sh -c
// <command>) can itself fork a child that inherits its stdout/stderr
// pipes — most concretely, git launching $EDITOR when a commit message
// is needed (a bare `commit`, or `rebase`/`merge`/`cherry-pick`
// --continue that resolves to a commit). If ctx is canceled while that
// grandchild is still running, exec.CommandContext's default Cancel
// (Process.Kill, one PID) kills only the direct child; the grandchild
// survives as an orphan and can keep holding the pipe's write end open.
// Cmd.Wait() then blocks until every holder of that pipe closes it —
// which is not itself gated on ctx, so the tool call (and the whole
// turn) hangs indefinitely, immune to Ctrl+C, regardless of how
// promptly ctx was canceled.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to cmd's entire process group. Falls
// back to killing just the process leader if the group signal can't be
// delivered (e.g. a test-constructed *exec.Cmd that never called
// configureProcessGroup before Start).
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
