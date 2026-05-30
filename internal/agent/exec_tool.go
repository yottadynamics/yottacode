package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// RunBashTool runs a shell command in cwd via /bin/sh -c. Always
// requires approval. There is no sandbox today; for real isolation,
// run yottacode itself inside a container.
type RunBashTool struct {
	Cwd *CwdRef
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
	return fmt.Sprintf("run_bash: %s", a.Command)
}

// runBashMaxStreamBytes caps each of stdout/stderr at 1 MiB so a
// runaway command can't OOM the agent.
const runBashMaxStreamBytes = 1 << 20

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
	c := exec.CommandContext(ctx, "/bin/sh", "-c", a.Command)
	c.Dir = t.Cwd.Get()
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	err := c.Run()
	exit := c.ProcessState.ExitCode()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", fmt.Errorf("run_bash: %w", err)
	}
	return fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		exit, stdout.String(), stderr.String()), nil
}

// cappedWriter drops bytes past runBashMaxStreamBytes and emits a
// `[output truncated]` notice in-band so the model sees the cap.
type cappedWriter struct {
	buf *bytes.Buffer
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remaining := runBashMaxStreamBytes - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.buf.WriteString("\n…[output truncated]\n")
		return len(p), nil
	}
	return w.buf.Write(p)
}
