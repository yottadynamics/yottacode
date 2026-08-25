package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RunBashTool runs a shell command in cwd via /bin/sh -c. Always requires
// approval. Command execution routes through Sandbox — nil selects
// HostSandbox (today's direct-on-host behavior); a config/experimental-flag
// gated PodmanSandbox (internal/sandbox) provides real isolation.
type RunBashTool struct {
	Cwd *CwdRef
	// Sandbox is nil-safe: a nil Sandbox behaves exactly like HostSandbox,
	// so every call site that doesn't set it keeps today's behavior.
	Sandbox Sandbox
}

// sandbox returns t.Sandbox, or HostSandbox{} when unset.
func (t *RunBashTool) sandbox() Sandbox {
	if t.Sandbox != nil {
		return t.Sandbox
	}
	return HostSandbox{}
}

func (t *RunBashTool) Name() string { return "run_bash" }

func (t *RunBashTool) Description() string {
	return "Execute a bash command in the working directory. Returns stdout, stderr, and exit code."
}

func (t *RunBashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to run, interpreted by /bin/sh -c",
			},
		},
		"required": []string{"command"},
	}
}

func (t *RunBashTool) RequiresApproval(string) bool { return true }

func (t *RunBashTool) PreviewCall(argsJSON string) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	preview := fmt.Sprintf("run_bash: %s", a.Command)
	if sb := t.sandbox(); sb.Label() != (HostSandbox{}).Label() {
		preview = sb.Label() + " " + preview
	}
	return preview
}

// runBashMaxStreamBytes caps each of stdout/stderr so a runaway
// command (e.g. `gh run view --log-failed` on a noisy CI job) can't
// dump megabytes of raw text into the model's context. Matches the
// 256 KiB ceiling used elsewhere for tool output (mediaMaxOutputBytes,
// web_tools' fetch cap).
const runBashMaxStreamBytes = 256 * 1024

func (t *RunBashTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("run_bash: invalid args: %w", err)
	}
	if a.Command == "" {
		return "", fmt.Errorf("run_bash: command is required")
	}
	// Hardline floor: a handful of catastrophic commands are refused
	// here, at the execution chokepoint, regardless of approval mode —
	// even under --yolo / BypassPermissions / background auto-approval.
	// Returned as a recoverable tool result (nil error) so the model sees
	// the refusal and can adapt; mirrors hermes's hardline blocklist and
	// Claude Code's rm -rf / circuit breaker.
	if blocked, reason := IsHardlineCommand(a.Command); blocked {
		return fmt.Sprintf("BLOCKED (hardline): %s. This command is on the unconditional blocklist and cannot be run through the agent — not even with --yolo. If you genuinely need it, run it yourself in a terminal outside the agent.", reason), nil
	}
	c := t.sandbox().Command(ctx, a.Command, t.Cwd.Get())
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	err := c.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		// Start-failure path (shell missing, cwd deleted, fd exhaustion):
		// the process never ran, so ProcessState is nil — reading the
		// exit code here would panic. Past this check the command either
		// succeeded or exited nonzero, and ProcessState is always set.
		return "", fmt.Errorf("run_bash: %w", err)
	}
	exit := c.ProcessState.ExitCode()
	result := fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		exit, stdout.String(), stderr.String())
	if exit == podmanInfraExitCode && isPodmanSandboxLabel(t.sandbox().Label()) {
		// podman's own exit-code convention (125 = podman itself failed —
		// bad container/cwd/image/network setup; 126/127 = the invoked
		// command couldn't be found/run; anything else passes the
		// contained command's own exit code straight through). Without
		// this note, a dead or misconfigured sandbox container surfaces
		// as an ordinary-looking exit=125 with podman's own error text
		// sitting in the stderr slot, indistinguishable in shape from
		// the user's own command failing — nothing tells the model this
		// is an infrastructure problem rather than a scripting one.
		result = "NOTE: exit=125 is podman's own convention for a podman-level failure (not the command's exit code) — the sandbox container itself may need attention (see /sandbox). " + result
	}
	return result, nil
}

// podmanInfraExitCode and podmanSandboxLabel encode just enough of
// PodmanSandbox's convention to annotate this one case, without
// internal/agent importing internal/sandbox (internal/agent stays
// unaware of any concrete Sandbox implementation — see
// internal/sandbox/podman.go's package doc) — Label() is already part
// of the Sandbox interface, so comparing against it costs nothing new.
const (
	podmanInfraExitCode = 125
	podmanSandboxLabel  = "[podman-sandbox]"
)

func isPodmanSandboxLabel(label string) bool {
	return label == podmanSandboxLabel || strings.HasPrefix(label, "[podman:")
}

// cappedWriter drops bytes past runBashMaxStreamBytes and emits a
// `[output truncated]` notice in-band so the model sees the cap.
type cappedWriter struct {
	buf       *bytes.Buffer
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.truncated {
		return len(p), nil
	}
	remaining := runBashMaxStreamBytes - w.buf.Len()
	if remaining <= 0 {
		w.buf.WriteString("\n…[output truncated]\n")
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.buf.WriteString("\n…[output truncated]\n")
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}
