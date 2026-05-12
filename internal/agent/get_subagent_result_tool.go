package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// getSubagentResultMaxWait is the upper bound on wait_seconds the
// tool will honor. Above this, the call would tie up the parent
// loop's goroutine for so long that a stuck subagent becomes a
// session-level hang. 10 minutes is generous for most real
// subagent workloads; anything longer is a workflow that should
// poll explicitly or chunk the work.
const getSubagentResultMaxWait = 10 * time.Minute

// getSubagentResultDefaultWait is the wait applied when the model
// doesn't pass an explicit wait_seconds. Defaults to 60s because
// the model otherwise tends to call get_subagent_result without
// wait_seconds, get "still running," and pivot to doing the
// subagent's work itself — wasting both the subagent's tokens AND
// duplicating effort. Making wait the default behavior removes
// the cliff: the call just waits, like every other tool call,
// and the model gets either the result or a definitive
// "still-running-after-60s" signal.
const getSubagentResultDefaultWait = 60 * time.Second

// GetSubagentResultTool retrieves a previously-dispatched subagent's
// state and final reply from the session task registry. The intended
// flow:
//
//  1. Parent calls Agent(...) with run_in_background:true → tool
//     returns a task id handle immediately.
//  2. Parent's turn ends; user keeps working; child runs to
//     completion in a goroutine.
//  3. Some turns later, when the user asks about the subagent's
//     findings, the parent calls get_subagent_result(task_id=<id>)
//     and the final reply lands as a normal tool result that
//     flows back into the parent's adapter context.
//
// Without this tool, background subagents are fire-and-forget: the
// transcript lives on disk and in the registry, but the parent's
// model has no programmatic way to pull a completed result back
// into the conversation. The pairing with run_in_background is
// what makes background runs actually useful.
//
// Read-only and ParallelSafe — safe to call repeatedly, safe to call
// alongside other read-only tools in the same model turn.
type GetSubagentResultTool struct {
	// Tasks is the session-scoped subagent task registry. The same
	// pointer the AgentTool uses to record spawns; pointer-shared
	// so we observe live-updated state.
	Tasks *subagents.Registry
}

func (t *GetSubagentResultTool) Name() string { return "get_subagent_result" }

func (t *GetSubagentResultTool) Description() string {
	return "Retrieve the final result of a previously-dispatched subagent task. " +
		"Pass a task id (or unique prefix; 8 chars is usually enough) returned by " +
		"a previous Agent call with run_in_background:true. " +
		"By default the tool BLOCKS for up to 60 seconds waiting for the subagent " +
		"to complete — so you can call this immediately after spawning a " +
		"background subagent and the result will arrive in one tool call without " +
		"polling. Pass wait_seconds to override (e.g. 300 for slow audits; capped " +
		"at 600). The tool returns as soon as the subagent finishes, OR after the " +
		"wait expires with a \"still running\" status hint. Read-only and idempotent."
}

func (t *GetSubagentResultTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "The task id (or unique prefix) of the subagent run to fetch. The Agent tool returns this id in its result when called with run_in_background:true.",
			},
			"wait_seconds": map[string]any{
				"type":        "integer",
				"description": "Override the default 60-second block: maximum seconds to wait for the subagent to complete before returning a status hint. Use a larger value (60–600) for slow investigations. Pass a smaller value (or omit and accept the 60s default) for normal lookups. The tool unblocks immediately when the subagent finishes regardless of this value.",
			},
		},
		"required": []string{"task_id"},
	}
}

// RequiresApproval is false: the tool only reads from the in-memory
// task registry and produces a string — no disk mutation, no
// network calls, no shell. Safe to auto-execute on every call.
func (t *GetSubagentResultTool) RequiresApproval(string) bool { return false }

// ParallelSafe lets the model fetch several subagent results in
// one turn — useful for "summarize all the background investigations
// I started" workflows.
func (t *GetSubagentResultTool) ParallelSafe(string) bool { return true }

func (t *GetSubagentResultTool) PreviewCall(argsJSON string) string {
	a := parseGetSubagentResultArgs(argsJSON)
	return fmt.Sprintf("get_subagent_result(%s)", truncateForPreview(a.TaskID, 16))
}

type getSubagentResultArgs struct {
	TaskID      string `json:"task_id"`
	WaitSeconds int    `json:"wait_seconds"`
}

func parseGetSubagentResultArgs(argsJSON string) getSubagentResultArgs {
	var a getSubagentResultArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a
}

func (t *GetSubagentResultTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	a := parseGetSubagentResultArgs(argsJSON)
	if strings.TrimSpace(a.TaskID) == "" {
		return "", fmt.Errorf("get_subagent_result: task_id is required")
	}
	if t.Tasks == nil {
		// Defensive: registry should always be wired by the TUI /
		// oneshot setup paths. If it isn't, treat as a recoverable
		// runtime error the model can adapt around.
		return "error: subagent registry is not configured in this session", nil
	}
	task, ok := t.Tasks.FindByPrefix(strings.TrimSpace(a.TaskID))
	if !ok {
		return fmt.Sprintf("error: no subagent task matches %q (or the prefix is ambiguous — use more characters from the id, typically 8 are enough)", a.TaskID), nil
	}

	// Block on the registry's completion channel until either the
	// task finishes or the wait expires. This replaces a polling
	// loop at the model layer with a single push-style wait at the
	// Go layer — the subagent's MarkDone closes the waiter channel,
	// which unblocks the tool, which delivers the result to the
	// parent's adapter context in one round-trip.
	//
	// The default wait is 60s (see getSubagentResultDefaultWait).
	// The model can override via wait_seconds. We deliberately do
	// NOT support "no wait" — calling get_subagent_result without
	// waiting was the failure mode that prompted this design: the
	// model would call, get "still running," and immediately pivot
	// to doing the subagent's work itself, wasting the subagent.
	if task.Status == subagents.TaskRunning {
		waitDur := time.Duration(a.WaitSeconds) * time.Second
		if waitDur <= 0 {
			waitDur = getSubagentResultDefaultWait
		}
		if waitDur > getSubagentResultMaxWait {
			waitDur = getSubagentResultMaxWait
		}
		waiter := t.Tasks.WaitFor(task.ID)
		select {
		case <-waiter:
			// Task finished (or was already terminal — WaitFor
			// returns a closed channel in that case). Re-fetch the
			// snapshot so we render the final state, not the stale
			// running one captured above.
			if updated, ok := t.Tasks.Get(task.ID); ok {
				task = updated
			}
		case <-time.After(waitDur):
			// Timeout — render whatever the current state is.
			// Falls through to the normal still-running formatter
			// below if the task is genuinely stuck.
			if updated, ok := t.Tasks.Get(task.ID); ok {
				task = updated
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	var b strings.Builder
	mode := "foreground"
	if task.Background {
		mode = "background"
	}
	fmt.Fprintf(&b, "Subagent task %s — %s (%s)\n", task.ID[:8], task.AgentType, mode)
	fmt.Fprintf(&b, "Status:    %s\n", task.Status)
	fmt.Fprintf(&b, "Duration:  %s\n", task.Duration())
	fmt.Fprintf(&b, "Tool calls: %d\n", task.ToolCalls)
	if task.TokensUsed > 0 {
		fmt.Fprintf(&b, "Tokens:    ~%d\n", task.TokensUsed)
	}
	if task.TranscriptPath != "" {
		fmt.Fprintf(&b, "Transcript: %s\n", task.TranscriptPath)
	}
	fmt.Fprintln(&b)

	switch task.Status {
	case subagents.TaskRunning:
		// Don't ship a result yet — the child is still working. List
		// the most recent activity so the parent can decide whether
		// to wait or move on. The activity slice is already
		// ring-buffered to 64 entries in the registry, so this stays
		// bounded.
		b.WriteString("--- Status: still running ---\n")
		b.WriteString("Call this tool again later to fetch the final result. ")
		b.WriteString("Recent activity (oldest first):\n")
		if len(task.Activities) == 0 {
			b.WriteString("  (no tool calls yet)\n")
		} else {
			for _, act := range task.Activities {
				fmt.Fprintf(&b, "  · %s\n", act)
			}
		}
	case subagents.TaskCompleted:
		b.WriteString("--- Final reply ---\n")
		b.WriteString(task.Result)
		if !strings.HasSuffix(task.Result, "\n") {
			b.WriteString("\n")
		}
	case subagents.TaskErrored, subagents.TaskIterCapped:
		b.WriteString("--- Result (errored) ---\n")
		b.WriteString(task.Result)
		if !strings.HasSuffix(task.Result, "\n") {
			b.WriteString("\n")
		}
	case subagents.TaskCanceled:
		b.WriteString("--- Result (canceled) ---\n")
		if task.CanceledByUser {
			b.WriteString("Canceled by user via /subagents stop. ")
		} else {
			b.WriteString("Canceled by parent-turn cancellation or context deadline. ")
		}
		if task.Result != "" {
			b.WriteString("Partial result:\n")
			b.WriteString(task.Result)
		}
	}
	return b.String(), nil
}

// truncateForPreview cuts s to n chars with a trailing ellipsis when
// it overflows. Mirrors agent_tool.go's `truncate` but local to this
// file so the tool is self-contained.
func truncateForPreview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
