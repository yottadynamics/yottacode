//go:build !linux

package mcp

import "os/exec"

// setProcAttr is a no-op on non-Linux platforms. PR_SET_PDEATHSIG is
// Linux-only; darwin and the BSDs have no equivalent that the Go
// stdlib exposes uniformly. An ungraceful yottacode exit on these
// platforms can leave MCP subprocesses parented to launchd / init —
// document the gap in docs/mcp.md rather than reinventing a per-OS
// cleanup mechanism here.
func setProcAttr(_ *exec.Cmd) {}
