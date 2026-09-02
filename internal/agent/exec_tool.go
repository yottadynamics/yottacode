package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/sandboxcache"
)

// RunBashTool runs a shell command in cwd via /bin/sh -c. Always requires
// approval. Command execution routes through Sandbox — nil selects
// HostSandbox (today's direct-on-host behavior); a configured PodmanSandbox
// (internal/sandbox) provides real isolation.
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
	runCommand, err := prepareRunBashCommand(a.Command, t.sandbox(), t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("run_bash: %w", err)
	}

	c := t.sandbox().Command(ctx, runCommand, t.Cwd.Get())
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	runErr := c.Run()
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		// Start-failure path (shell missing, cwd deleted, fd exhaustion):
		// the process never ran, so ProcessState is nil — reading the
		// exit code here would panic. Past this check the command either
		// succeeded or exited nonzero, and ProcessState is always set.
		return "", fmt.Errorf("run_bash: %w", runErr)
	}
	exit := c.ProcessState.ExitCode()
	result := fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		exit, stdout.String(), stderr.String())
	return podmanInfraNote(t.sandbox(), exit, result), nil
}

func prepareRunBashCommand(command string, sandbox Sandbox, root string) (string, error) {
	if sandbox.Label() != (HostSandbox{}).Label() || !runBashEnvLeaksIntoRoot(root) {
		return command, nil
	}
	base := filepath.Clean(root)
	shellScratch, err := sandboxcache.HostShellScratchDir(base)
	if err != nil {
		return "", err
	}
	shellTmp := filepath.Join(shellScratch, "tmp")
	xdgCache := filepath.Join(shellScratch, "xdg-cache")
	xdgConfig := filepath.Join(shellScratch, "xdg-config")
	xdgData := filepath.Join(shellScratch, "xdg-data")
	xdgState := filepath.Join(shellScratch, "xdg-state")
	return joinRunBashEnv(command, shellScratch, shellTmp, xdgCache, xdgConfig, xdgData, xdgState), nil
}

func runBashEnvLeaksIntoRoot(root string) bool {
	for _, key := range []string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		if pathIsWithinRoot(os.Getenv(key), root) {
			return true
		}
	}
	return false
}

func pathIsWithinRoot(path, root string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if absPath == absRoot {
		return true
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func joinRunBashEnv(command, home, tmp, xdgCache, xdgConfig, xdgData, xdgState string) string {
	// Host run_bash executes arbitrary approved commands directly on the
	// workstation. Keep HOME/XDG/TMPDIR away from the checkout so tools that
	// follow freedesktop defaults (GitHub CLIs, Podman helpers, language tools)
	// cannot leak repo-root .local/.cache/.config directories just because the
	// parent process inherited a bad HOME or XDG value.
	return strings.Join([]string{
		"mkdir -p " + strings.Join([]string{shellQuoteSingle(tmp), shellQuoteSingle(xdgCache), shellQuoteSingle(xdgConfig), shellQuoteSingle(xdgData), shellQuoteSingle(xdgState)}, " "),
		"export HOME=" + shellQuoteSingle(home) + " TMPDIR=" + shellQuoteSingle(tmp) + " XDG_CACHE_HOME=" + shellQuoteSingle(xdgCache) + " XDG_CONFIG_HOME=" + shellQuoteSingle(xdgConfig) + " XDG_DATA_HOME=" + shellQuoteSingle(xdgData) + " XDG_STATE_HOME=" + shellQuoteSingle(xdgState),
		command,
	}, " && ")
}

// podmanInfraNote prepends podman's own exit-code-125 disambiguation note to
// result when exit==125 AND sb is actually podman-backed (matched by Label(),
// not type — internal/agent stays unaware of any concrete Sandbox
// implementation, see internal/sandbox/podman.go's package doc). 125 is podman
// itself failing (bad container/cwd/image/network setup), not the contained
// command's own exit code; 126/127 mean the invoked command couldn't be
// found/run; anything else passes the contained command's exit code straight
// through. Without this note, a dead or misconfigured sandbox container surfaces
// as an ordinary-looking exit=125 with podman's own error text sitting in the
// stderr slot, indistinguishable in shape from the command itself failing.
// Shared by RunBashTool and RunTestsTool, the two tools whose commands can run
// inside the sandbox.
func podmanInfraNote(sb Sandbox, exit int, result string) string {
	if exit == podmanInfraExitCode && isPodmanSandboxLabel(sb.Label()) {
		return "NOTE: exit=125 is podman's own convention for a podman-level failure (not the command's exit code) — the sandbox container itself may need attention (see /sandbox). " + result
	}
	return result
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
