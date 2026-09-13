//go:build unix

package execguard

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts cmd in its own process group so
// killProcessGroup can signal every descendant it spawns, not just the
// direct child exec.CommandContext's default Cancel would otherwise
// kill alone. Mirrors internal/lsp/process_unix.go and
// internal/agent/procgroup_unix.go's identical pattern.
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
