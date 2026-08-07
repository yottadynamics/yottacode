package agent

import (
	"context"
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
