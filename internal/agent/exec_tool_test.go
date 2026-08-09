package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestRunBashTool_EchoCapturesStdout(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"echo yes"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("output missing exit=0: %q", out)
	}
	if !strings.Contains(out, "yes") {
		t.Errorf("output missing stdout: %q", out)
	}
}

func TestRunBashTool_ReportsNonZeroExit(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"exit 42"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=42") {
		t.Errorf("output = %q, want exit=42", out)
	}
}

// Regression: when the shell can't start at all (here: the tool's
// working directory no longer exists), Run() fails before a process
// is created and ProcessState stays nil. The exit-code read used to
// happen before the error check and panicked the whole turn.
func TestRunBashTool_ShellStartFailureReturnsError(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef("/nonexistent/yottacode-gone-dir")}
	out, err := tool.Execute(context.Background(), `{"command":"echo unreachable"}`)
	if err == nil {
		t.Fatalf("expected start-failure error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "run_bash:") {
		t.Errorf("error not wrapped with tool name: %v", err)
	}
}

// Regression: a noisy command (e.g. `gh run view --log-failed` on a
// verbose CI job) must not be allowed to dump megabytes of text into
// the model's context. 300000 bytes exceeds the 256 KiB cap but was
// let through whole under the old 1 MiB ceiling.
func TestRunBashTool_TruncatesLargeStdout(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"yes a | head -c 300000"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[output truncated]") {
		t.Errorf("expected truncation marker, got output of length %d", len(out))
	}
	if len(out) > runBashMaxStreamBytes+1024 {
		t.Errorf("output length %d exceeds cap (%d) plus overhead", len(out), runBashMaxStreamBytes)
	}
}

func TestRunBashTool_RejectsEmptyCommand(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error on empty command")
	}
}

func TestRunBashTool_BadJSON(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Errorf("expected error on bad JSON")
	}
}

// spySandbox records the (command, cwd) it was asked to run, then
// delegates to HostSandbox so Execute still gets real output — this lets
// a test assert both "the seam was used correctly" and "the command still
// actually ran," without a fake process.
type spySandbox struct {
	label      string
	gotCommand string
	gotCwd     string
	callCount  int
	closeCount int
}

func (s *spySandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	s.callCount++
	s.gotCommand, s.gotCwd = command, cwd
	return HostSandbox{}.Command(ctx, command, cwd)
}
func (s *spySandbox) Label() string { return s.label }
func (s *spySandbox) Close() error  { s.closeCount++; return nil }

// Nil Sandbox must behave exactly like an explicit HostSandbox{} — this is
// the back-compat contract every existing no-Sandbox call site relies on.
func TestRunBashTool_NilSandboxDefaultsToHost(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	if _, ok := tool.sandbox().(HostSandbox); !ok {
		t.Errorf("nil Sandbox should default to HostSandbox, got %T", tool.sandbox())
	}
}

// Execute must route the command and cwd through Sandbox.Command rather
// than building exec.Command inline — this is the seam's whole point.
func TestRunBashTool_ExecuteRoutesThroughSandbox(t *testing.T) {
	dir := t.TempDir()
	spy := &spySandbox{label: "[podman]"}
	tool := &RunBashTool{Cwd: NewCwdRef(dir), Sandbox: spy}
	out, err := tool.Execute(context.Background(), `{"command":"echo via-sandbox"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if spy.callCount != 1 {
		t.Errorf("Sandbox.Command called %d times, want 1", spy.callCount)
	}
	if spy.gotCommand != "echo via-sandbox" {
		t.Errorf("Sandbox.Command got command %q", spy.gotCommand)
	}
	if spy.gotCwd != dir {
		t.Errorf("Sandbox.Command got cwd %q, want %q", spy.gotCwd, dir)
	}
	if !strings.Contains(out, "via-sandbox") {
		t.Errorf("output missing command's stdout: %q", out)
	}
}

// PreviewCall prefixes the Sandbox's label for scrollback, but only when
// it differs from HostSandbox's — the common (unsandboxed) case must not
// grow a "[no sandbox]" prefix on every single run_bash preview.
func TestRunBashTool_PreviewCallLabelsNonHostSandbox(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: &spySandbox{label: "[podman]"}}
	got := tool.PreviewCall(`{"command":"ls"}`)
	if !strings.HasPrefix(got, "[podman] ") {
		t.Errorf("PreviewCall = %q, want [podman] prefix", got)
	}
}

func TestRunBashTool_PreviewCallOmitsHostLabel(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	got := tool.PreviewCall(`{"command":"ls"}`)
	if strings.HasPrefix(got, "[") {
		t.Errorf("PreviewCall = %q, unsandboxed preview should not carry a bracket tag", got)
	}
}

// exitCodeSandbox is a minimal Sandbox test double whose Command always
// runs `sh -c "exit <code>"`, ignoring the requested command entirely —
// used to pin exit-code-125 annotation behavior without needing a real
// podman container.
type exitCodeSandbox struct {
	label string
	code  int
}

func (s *exitCodeSandbox) Command(ctx context.Context, _, _ string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", s.code))
}
func (s *exitCodeSandbox) Label() string { return s.label }
func (s *exitCodeSandbox) Close() error  { return nil }

// TestRunBashTool_Exit125AnnotatedOnlyForPodman: podman's own exit-code
// convention (125 = podman itself failed, not the contained command) is
// only meaningful when the active Sandbox actually IS podman-backed —
// annotating it for a hypothetical other Sandbox whose Label() isn't
// "[podman-sandbox]", or for an ordinary command that happens to exit 125
// on the host, would be a false diagnostic.
func TestRunBashTool_Exit125AnnotatedOnlyForPodman(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: &exitCodeSandbox{label: "[podman-sandbox]", code: 125}}
	out, err := tool.Execute(context.Background(), `{"command":"anything"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "NOTE: exit=125 is podman's own convention") {
		t.Errorf("expected the podman exit=125 note, got: %q", out)
	}
	if !strings.Contains(out, "exit=125") {
		t.Errorf("the underlying exit=125 line should still be present, got: %q", out)
	}
}

func TestRunBashTool_Exit125NotAnnotatedForHost(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())} // nil Sandbox -> HostSandbox
	out, err := tool.Execute(context.Background(), `{"command":"exit 125"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "NOTE: exit=125") {
		t.Errorf("host execution exiting 125 is an ordinary command exit code, not a podman failure — got: %q", out)
	}
}

func TestRunBashTool_Exit125NotAnnotatedForNonPodmanSandbox(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: &exitCodeSandbox{label: "[other]", code: 125}}
	out, err := tool.Execute(context.Background(), `{"command":"anything"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "NOTE: exit=125") {
		t.Errorf("the exit=125 convention is podman-specific, should not fire for a differently-labeled sandbox — got: %q", out)
	}
}
