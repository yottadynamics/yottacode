package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TodoWriteTool is the yottacode analogue of Claude Code's TodoWrite:
// a model-callable tool that replaces the working plan on every call,
// with one item allowed to be `in_progress` at a time. Rendering and
// persistence happen one layer out — this tool just owns the
// validated write into the PlanStore. The loop notices the write
// via the planAware interface (see loop.go) and emits a TodoUpdate
// event for the TUI.
type TodoWriteTool struct {
	Store *PlanStore
}

func (t *TodoWriteTool) Name() string { return "todo_write" }

func (t *TodoWriteTool) Description() string {
	return "Maintain the agent's working task plan, visible to the user as a card in the transcript. " +
		"Pass the COMPLETE plan every call — the previous plan is discarded, not merged. " +
		"Statuses: 'pending' (not started), 'in_progress' (currently working on it; AT MOST ONE), 'completed' (done). " +
		"\n\nUse this tool PROACTIVELY for any task that has 3 or more distinct steps. " +
		"Call it BEFORE you start work to lay out the plan, then call it AGAIN as soon as each step finishes — " +
		"flip the just-finished item to 'completed' and move the next item to 'in_progress' in the SAME call. " +
		"On multi-step work, calling this tool only once (or not at all) leaves the user blind to your progress and is a mistake. " +
		"\n\nIMPORTANT: When a task has both a design/research phase and an execution phase (writing code, running commands, modifying files), the todo list MUST end at the design-presentation step. " +
		"Do NOT include implementation steps in the same plan. After the design phase completes, present the design to the user and wait for explicit agreement before calling todo_write again with implementation steps. " +
		"Queueing implementation as todos pre-stages work the user has not approved. " +
		"\n\nDo NOT use this tool for trivial single-step requests (one read, one edit, a quick answer) — the plan card just adds noise there. " +
		"Pass an empty list to clear the plan when the work is done or no longer relevant."
}

func (t *TodoWriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "The complete plan. Replaces the previous list wholesale.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "Short human-readable description of the step.",
						},
						"status": map[string]any{
							"type":        "string",
							"description": "Lifecycle state of this step.",
							"enum":        []string{"pending", "in_progress", "completed"},
						},
					},
					"required": []string{"content", "status"},
				},
			},
		},
		"required": []string{"todos"},
	}
}

// RequiresApproval is always false: this tool has no filesystem,
// network, or external side effects — it's purely a visibility
// primitive that updates a per-session in-memory list. Matches
// Claude Code's TodoWrite posture exactly: real safety comes from
// the per-mutation prompts on edit_file, write_file, run_bash, etc.,
// not from gating the plan itself.
func (t *TodoWriteTool) RequiresApproval(string) bool { return false }

// PlanStore exposes the underlying store so the agent loop can
// snapshot the list after this tool runs and emit a TodoUpdate event.
// Satisfies the unexported planAware interface in loop.go.
func (t *TodoWriteTool) PlanStore() *PlanStore { return t.Store }

type todoWriteArgs struct {
	Todos []Todo `json:"todos"`
}

func (t *TodoWriteTool) PreviewCall(argsJSON string) string {
	var a todoWriteArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	done := 0
	for _, td := range a.Todos {
		if td.Status == TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("Plan: %d items (%d done)", len(a.Todos), done)
}

func (t *TodoWriteTool) Execute(_ context.Context, argsJSON string) (string, error) {
	if t.Store == nil {
		return "", fmt.Errorf("todo_write: no PlanStore wired")
	}
	var a todoWriteArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("todo_write: invalid args: %w", err)
	}
	cleaned, err := validatePlanItems(a.Todos)
	if err != nil {
		return "", err
	}
	t.Store.Replace(cleaned)
	done := 0
	for _, td := range cleaned {
		if td.Status == TodoCompleted {
			done++
		}
	}
	if len(cleaned) == 0 {
		return "plan cleared", nil
	}
	return fmt.Sprintf("plan updated: %d items (%d done)", len(cleaned), done), nil
}

func validatePlanItems(items []Todo) ([]Todo, error) {
	out := make([]Todo, 0, len(items))
	inProgress := 0
	for i, td := range items {
		content := strings.TrimSpace(td.Content)
		if content == "" {
			return nil, fmt.Errorf("todo_write: item %d has empty content", i)
		}
		switch td.Status {
		case TodoPending, TodoInProgress, TodoCompleted:
		case "":
			return nil, fmt.Errorf("todo_write: item %d has empty status", i)
		default:
			return nil, fmt.Errorf("todo_write: item %d has unknown status %q (allowed: pending, in_progress, completed)", i, td.Status)
		}
		if td.Status == TodoInProgress {
			inProgress++
		}
		out = append(out, Todo{Content: content, Status: td.Status})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("todo_write: at most one item may be in_progress at a time (got %d)", inProgress)
	}
	return out, nil
}
