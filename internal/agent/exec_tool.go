package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	// Jobs hosts background shell jobs (background=true, or a timeout above the
	// foreground cap). Nil — oneshot, dispatch workers — means every command
	// runs in the foreground and the background parameters are not offered.
	Jobs *ShellJobs
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
	d := fmt.Sprintf("Execute a bash command in the working directory. Returns stdout, stderr, and exit code. "+
		"A foreground command is killed (whole process group) after timeout_ms: default %ds, maximum %ds. "+
		"Prefer the grep and glob tools over `grep -R`/`find` in shell: recursive shell searches can block "+
		"on named pipes or sockets until the timeout.",
		int(runBashDefaultTimeout/time.Second), int(runBashMaxTimeout/time.Second))
	if t.Jobs.Available() {
		d += " For work that must outlive the call (dev servers, watchers, long builds) set background=true: " +
			"it returns a session_id at once and the job keeps running; manage it with the process tool " +
			"(poll, wait, log, kill). Add notify_on_complete=true to be told when it exits instead of polling. " +
			"A timeout_ms above the maximum is not refused: the command is promoted to a background job with notify_on_complete. " +
			"Do not use background for sleeps, timers, or polling loops."
	}
	return d
}

func (t *RunBashTool) Schema() map[string]any {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to run, interpreted by /bin/sh -c",
			},
			"timeout_ms": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf("Optional timeout in milliseconds. Default %d, maximum %d; larger values are clamped.",
					runBashDefaultTimeout.Milliseconds(), runBashMaxTimeout.Milliseconds()),
			},
		},
		"required": []string{"command"},
	}
	if t.Jobs.Available() {
		props := schema["properties"].(map[string]any)
		props["background"] = map[string]any{
			"type":        "boolean",
			"description": "Run in the background and return a session_id immediately. Manage it with the process tool. Foreground commands are killed at the timeout; use this for anything that must outlive the call.",
		}
		props["notify_on_complete"] = map[string]any{
			"type":        "boolean",
			"description": "Background only: re-prompt me with the exit code and output tail when the job exits. Omit for jobs you will poll or that run until killed.",
		}
	}
	return schema
}

func (t *RunBashTool) RequiresApproval(string) bool { return true }

func (t *RunBashTool) PreviewCall(argsJSON string) string {
	var a struct {
		Command    string `json:"command"`
		Background bool   `json:"background"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	preview := fmt.Sprintf("run_bash: %s", a.Command)
	if a.Background {
		preview += " (background)"
	}
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

// Foreground run_bash deadline. Vars (not consts) so tests can exercise the
// clamp without waiting out real wall-clock time; production code never
// reassigns them.
var (
	runBashDefaultTimeout = 3 * time.Minute
	runBashMaxTimeout     = 10 * time.Minute
)

// runBashTimeoutExit is the exit code reported for a timed-out foreground
// command, matching GNU timeout(1).
const runBashTimeoutExit = 124

// runBashTimeout resolves the model-supplied timeout_ms: absent or
// non-positive selects the default, anything above the cap is clamped.
func runBashTimeout(timeoutMS *int64) time.Duration {
	if timeoutMS == nil || *timeoutMS <= 0 {
		return runBashDefaultTimeout
	}
	// Compare in milliseconds so a huge value can't overflow time.Duration.
	if *timeoutMS >= runBashMaxTimeout.Milliseconds() {
		return runBashMaxTimeout
	}
	return time.Duration(*timeoutMS) * time.Millisecond
}

func (t *RunBashTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Command          string `json:"command"`
		TimeoutMS        *int64 `json:"timeout_ms"`
		Background       bool   `json:"background"`
		NotifyOnComplete bool   `json:"notify_on_complete"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("run_bash: invalid args: %w", err)
	}
	if a.Command == "" {
		return "", fmt.Errorf("run_bash: command is required")
	}
	if a.NotifyOnComplete && !a.Background {
		return "", fmt.Errorf("run_bash: notify_on_complete only applies to background jobs; set background=true")
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
	if blocked, reason := IsPrivilegeEscalationCommand(a.Command); blocked {
		return fmt.Sprintf("BLOCKED (privilege escalation): %s. Commands that can prompt for an OS password are refused before execution so the terminal cannot hang on an agent-initiated sudo prompt. Run the command yourself in a terminal outside the agent, then ask yottacode to continue.", reason), nil
	}
	if recovery, err := t.Cwd.Recover(); err != nil {
		return "", fmt.Errorf("run_bash: cwd unavailable: %w", err)
	} else if recovery.Recovered {
		// The loop emits the user-facing CwdChanged event; direct tool users
		// still need the command to run from the repaired directory.
	}
	preparedCommand, err := prepareRunBashCommand(a.Command, t.sandbox(), t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("run_bash: %w", err)
	}

	// A foreground timeout above the cap is promoted to a tracked background job
	// rather than refused or clamped, so a legitimately long build is neither
	// killed nor lost. Where the session cannot host jobs it degrades to the
	// clamped foreground path below.
	background, notify, promoted := a.Background, a.NotifyOnComplete, false
	if !background && a.TimeoutMS != nil && *a.TimeoutMS > runBashMaxTimeout.Milliseconds() {
		if t.Jobs.Available() {
			background, notify, promoted = true, true, true
		} else {
			// Sessions without detached-job support still execute the command;
			// clamp to the foreground ceiling rather than silently rejecting it.
			clamped := runBashMaxTimeout.Milliseconds()
			a.TimeoutMS = &clamped
		}
	}
	if background {
		if !t.Jobs.Available() {
			return "run_bash: background mode is not available in this session; run the command in the foreground with a larger timeout_ms (max " +
				fmt.Sprint(runBashMaxTimeout.Milliseconds()) + ").", nil
		}
		job, err := t.Jobs.Start(t.sandbox(), preparedCommand, t.Cwd.Get(), notify)
		if err != nil {
			return "run_bash: could not start background job: " + err.Error(), nil
		}
		return backgroundStartResult(job, promoted), nil
	}

	timeout := runBashTimeout(a.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := t.sandbox().Command(runCtx, preparedCommand, t.Cwd.Get())
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}

	runErr := c.Run()
	// Our deadline fired and the caller did not cancel: report it as a
	// recoverable result carrying whatever output was captured, so the
	// model can retry with a narrower command instead of seeing a bare
	// kill signal. A parent cancel (Esc) leaves ctx.Err() set and falls
	// through to the normal path.
	if ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("exit=%d\nrun_bash: timed out after %s; the command and its child processes were killed. "+
			"Retry with a narrower command, or a larger timeout_ms (max %d)%s.\n--- stdout ---\n%s\n--- stderr ---\n%s",
			runBashTimeoutExit, timeout, runBashMaxTimeout.Milliseconds(), t.backgroundHint(), stdout.String(), stderr.String()), nil
	}
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

// backgroundHint is the extra remedy offered on a timeout when jobs exist.
func (t *RunBashTool) backgroundHint() string {
	if t.Jobs.Available() {
		return ", or run it with background=true if it is meant to run long"
	}
	return ""
}

// backgroundStartResult is what the model sees after a job starts: the id to
// manage it by, and an explicit statement of whether it will be told when the
// job ends, so it neither polls needlessly nor waits for a notice that will not
// come.
func backgroundStartResult(job *shellJob, promoted bool) string {
	var b strings.Builder
	if promoted {
		fmt.Fprintf(&b, "timeout_ms exceeds the %dms foreground maximum, so the command was promoted to a background job instead of being killed. Do not re-run it.\n", runBashMaxTimeout.Milliseconds())
	}
	fmt.Fprintf(&b, "started background job\nsession_id: %s\npid: %d\ncommand: %s\n", job.id, job.pid, oneLine(job.command))
	switch {
	case job.notify:
		b.WriteString("notify_on_complete: true. You will be notified with the exit code and output tail when it exits; do not poll for it.\n")
	case job.notifyDropped:
		b.WriteString("notify_on_complete: unavailable in this session (completion notices cannot be delivered here). You will NOT be told when it exits; use process(action=\"wait\" or \"poll\").\n")
	default:
		b.WriteString("notify_on_complete: false. You will NOT be told when it exits; use process(action=\"wait\" or \"poll\") to check on it.\n")
	}
	b.WriteString("Stop it with process(action=\"kill\"). Background jobs end when the session closes.")
	return b.String()
}

func prepareRunBashCommand(command string, sandbox Sandbox, root string) (string, error) {
	withGo := runTestsCommandUsesGo(command)
	// Ordinary host commands should retain the user's real HOME and XDG config.
	// Redirect only when inherited state points into the checkout; Go commands
	// are always wrapped because the toolchain also writes implicit cache,
	// telemetry, and executable temporary files.
	if sandbox.Label() == (HostSandbox{}).Label() && !withGo && !runBashEnvLeaksIntoRoot(root) {
		return command, nil
	}
	env, err := commandEnvironment(root, sandbox.Label() != (HostSandbox{}).Label(), withGo)
	if err != nil {
		return "", err
	}
	return env.wrap(command, withGo), nil
}

func runBashEnvLeaksIntoRoot(root string) bool {
	for _, key := range []string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		if sandboxcache.PathWithinWorkspace(os.Getenv(key), root) {
			return true
		}
	}
	return false
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
