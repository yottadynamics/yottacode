//go:build linux

package mcp

import (
	"os/exec"
	"syscall"
)

// setProcAttr sets Linux-specific process attributes that protect the
// MCP subprocess against an ungraceful yottacode exit. Pdeathsig =
// SIGTERM means the kernel signals the child as soon as yottacode's
// PID disappears (SIGKILL of yottacode, crash, anything that skips
// Manager.Stop), so the child can't survive as an orphan reparented
// to PID 1.
//
// Not portable: PR_SET_PDEATHSIG is a Linux-only prctl. The
// proc_other.go sibling is a no-op for darwin and the BSDs; orphan
// cleanup on those platforms is the user's responsibility (or a
// future wedge that wires up libdispatch / kqueue equivalents).
func setProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
