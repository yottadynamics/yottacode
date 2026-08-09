package agent

import (
	"context"
	"os/exec"
)

// Sandbox is the command-execution seam RunBashTool routes every command
// through. Constructed once per session (or once per dispatch write-worker)
// and shared across every tool call built from that cwd — never rebuilt
// per call, so a container-backed implementation can stay session-scoped
// (podman exec per command, not podman run --rm per command).
//
// The hardline blocklist (IsHardlineCommand) is checked by RunBashTool
// BEFORE Command is ever called, and stays that way regardless of which
// Sandbox is active — it is not part of this seam.
type Sandbox interface {
	// Command returns the *exec.Cmd that will run `command` (a shell
	// command line interpreted by /bin/sh -c) with working directory cwd.
	// Callers set Stdout/Stderr and call Run/Start themselves.
	Command(ctx context.Context, command, cwd string) *exec.Cmd

	// Label identifies the backend for scrollback annotation. Contract a
	// new implementation must follow for the tag to actually render:
	//   - HostSandbox{}.Label() ("[no sandbox]") is the one value
	//     RunBashTool.PreviewCall treats as "don't prepend anything" —
	//     don't reuse it for a real backend, or its tag silently vanishes.
	//   - Every other label must be the exact form "[name]" — a leading
	//     "[", the name, trailing "]", NO trailing space (PreviewCall adds
	//     the separating space itself: `sb.Label() + " " + preview`).
	//     PreviewCall's output is then what the TUI's toolHeader
	//     (tool_card.go) recovers the tag from, by scanning for a leading
	//     "[...] " rather than through a structured field — a label
	//     outside this shape degrades safely (the tag is silently dropped
	//     from the card header) but never renders. See
	//     tool_card_test.go's TestToolHeader_RunBashCarriesSandboxTag.
	Label() string

	// Close tears down any backing resources (e.g. a session container).
	// Called once at session/worker teardown.
	Close() error
}

// HostSandbox runs commands directly on the host via /bin/sh -c — today's
// only behavior, reproduced verbatim so wrapping RunBashTool's exec.Command
// construction behind the Sandbox interface changes nothing when no other
// Sandbox is configured. The zero value of RunBashTool.Sandbox behaves
// identically to HostSandbox (see RunBashTool.sandbox).
type HostSandbox struct{}

func (HostSandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.Dir = cwd
	return c
}

func (HostSandbox) Label() string { return "[no sandbox]" }

func (HostSandbox) Close() error { return nil }

// SandboxFactory constructs a fresh, worker-scoped Sandbox for a dispatch
// write-worker's isolated git worktree. wtDir is the worker's worktree
// root (its mount point, if the returned Sandbox is container-backed);
// taskID identifies the worker for container naming/logging.
type SandboxFactory func(ctx context.Context, wtDir, taskID string) (Sandbox, error)
