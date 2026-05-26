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
