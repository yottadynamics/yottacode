package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// processWaitDefault / processWaitMax bound a single process(wait) call so a
	// stuck job can never pin a turn indefinitely; the model re-issues wait.
	processWaitDefault = 60 * time.Second
	processWaitMax     = 300 * time.Second
	// processLogDefaultLines / processLogMaxLines bound process(log).
	processLogDefaultLines = 200
	processLogMaxLines     = 2000
)

// ProcessTool manages background shell jobs started by run_bash(background).
// It only ever touches jobs this session started, so it needs no approval: the
// command itself was approved when it was started.
type ProcessTool struct {
	Jobs *ShellJobs
}

// processToolName is the registry name of ProcessTool.
const processToolName = "process"

func (t *ProcessTool) Name() string { return processToolName }

func (t *ProcessTool) Description() string {
	return "Manage background shell jobs started with run_bash(background=true): " +
		"poll (status plus new output, never blocks), wait (block until the job exits or timeout_ms elapses, " +
		fmt.Sprintf("default %ds, max %ds; re-issue to keep waiting), ", int(processWaitDefault/time.Second), int(processWaitMax/time.Second)) +
		"log (last lines of output), kill (stop the job). " +
		"Jobs end when the session closes. If the job was started with notify_on_complete you are told when it exits: do not poll for it."
}

func (t *ProcessTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"poll", "wait", "log", "kill"},
				"description": "poll: status + output since the last poll; wait: block until exit or timeout; log: tail of output; kill: stop the job",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by run_bash(background=true); an unambiguous prefix of at least 4 characters is accepted",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("wait only: how long to block, in milliseconds. Default %d, max %d.", processWaitDefault.Milliseconds(), processWaitMax.Milliseconds()),
			},
			"lines": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("log only: number of trailing lines. Default %d, max %d.", processLogDefaultLines, processLogMaxLines),
			},
		},
		"required": []string{"action", "session_id"},
	}
}

func (t *ProcessTool) RequiresApproval(string) bool { return false }

func (t *ProcessTool) PreviewCall(argsJSON string) string {
	var a struct {
		Action    string `json:"action"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("process: %s %s", a.Action, a.SessionID)
}

func (t *ProcessTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Action    string `json:"action"`
		SessionID string `json:"session_id"`
		TimeoutMS *int64 `json:"timeout_ms"`
		Lines     *int   `json:"lines"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("process: invalid args: %w", err)
	}
	if t.Jobs == nil {
		return "", fmt.Errorf("process: %w", ErrShellJobsUnavailable)
	}
	var (
		out string
		err error
	)
	switch a.Action {
	case "poll":
		out, err = t.Jobs.Poll(a.SessionID)
	case "wait":
		out, err = t.Jobs.Wait(ctx, a.SessionID, processWaitDuration(a.TimeoutMS))
	case "log":
		lines := processLogDefaultLines
		if a.Lines != nil && *a.Lines > 0 {
			lines = min(*a.Lines, processLogMaxLines)
		}
		out, err = t.Jobs.Log(a.SessionID, lines)
	case "kill":
		out, err = t.Jobs.Kill(ctx, a.SessionID)
	default:
		return "", fmt.Errorf("process: unknown action %q (want poll, wait, log or kill)", a.Action)
	}
	if err != nil {
		// A bad or expired id is something the model can recover from, so it is
		// a tool result, not a Go error.
		return "process: " + err.Error(), nil
	}
	return out, nil
}

// processWaitDuration resolves wait's timeout_ms: absent or non-positive uses
// the default, anything above the cap is clamped.
func processWaitDuration(timeoutMS *int64) time.Duration {
	if timeoutMS == nil || *timeoutMS <= 0 {
		return processWaitDefault
	}
	if *timeoutMS >= processWaitMax.Milliseconds() {
		return processWaitMax
	}
	return time.Duration(*timeoutMS) * time.Millisecond
}

// Cleanup satisfies CleanupTool so runtime shutdown stops every background job
// instead of orphaning it.
func (t *ProcessTool) Cleanup(ctx context.Context) error {
	if t.Jobs == nil {
		return nil
	}
	return t.Jobs.Cleanup(ctx)
}
