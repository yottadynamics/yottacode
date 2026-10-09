package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// DeepResearchTaskType is the registry/dock label of a background run.
const DeepResearchTaskType = "deep-research"

// canRunInBackground reports whether this session can host a detached
// workflow: an interactive TUI (AllowBackground) with a task registry for the
// dock, /subagents stop and the completion wake. Oneshot and ACP have neither,
// so they run the workflow inside the turn instead.
func (t *DeepResearchTool) canRunInBackground() bool {
	return t.Agent != nil && t.Agent.AllowBackground && t.Agent.Tasks != nil
}

// ErrDeepResearchForeground means this session cannot host a detached run
// (oneshot, ACP): callers fall back to the blocking tool path.
var ErrDeepResearchForeground = errors.New("deep research cannot run in the background in this session")

// StartBackground launches a run the way the tool does in the TUI, for callers
// that start it themselves (the /deep-research slash command) instead of via a
// model tool call. It returns the new task's id. ErrDeepResearchForeground
// means a detached run is not possible here; any other error is an admission
// failure (concurrency cap, resource pressure) to show the user as-is.
func (t *DeepResearchTool) StartBackground(query string, breadth int) (taskID string, err error) {
	if !t.canRunInBackground() {
		return "", ErrDeepResearchForeground
	}
	cwd := ""
	if t.Cwd != nil {
		cwd = t.Cwd.Get()
	}
	return t.launchBackground(query, clampBreadth(breadth), cwd)
}

// launchBackground registers the whole workflow as ONE background task and
// runs it on a detached goroutine, returning at once so the user keeps the
// prompt. It reuses the machinery background subagents already have:
//
//   - the dock shows the task, with the current phase as its activity line;
//   - /subagents stop and session shutdown cancel it through the registry;
//   - on completion MarkDone + fireBackgroundDone give the TUI its banner and
//     the summary, which the TUI delivers itself (no model turn).
//
// The researchers and verifiers are the same foreground-style children as in
// blocking mode (bounded by the same semaphore); only the coordinator is
// detached.
func (t *DeepResearchTool) launchBackground(query string, breadth int, cwd string) (string, error) {
	a := t.Agent
	taskID := subagents.NewTaskID()
	task := &subagents.Task{
		ID:           taskID,
		AgentType:    DeepResearchTaskType,
		Prompt:       query,
		Started:      time.Now(),
		Status:       subagents.TaskRunning,
		Background:   true,
		NotifyOnDone: true,
	}
	if a.TranscriptDir != "" {
		task.TranscriptPath = filepath.Join(a.TranscriptDir, fmt.Sprintf("%s-%s.md", DeepResearchTaskType, taskID))
	}
	// One active run per session. Concurrent runs would share the foreground
	// cap (starving each other's researchers into admission errors), blur the
	// per-run token attribution, and race for report names. The lock makes the
	// check and the slot reservation below one atomic step.
	t.startMu.Lock()
	defer t.startMu.Unlock()
	for _, other := range a.Tasks.List() {
		if other.AgentType == DeepResearchTaskType && other.Status == subagents.TaskRunning {
			return "", fmt.Errorf("a deep-research run is already in progress (task %s); wait for it to finish or stop it with /subagents", other.ID[:min(8, len(other.ID))])
		}
	}
	if err := backgroundResourcePreflight(); err != nil {
		return "", fmt.Errorf("deep research admission denied: %w; stop completed/stuck work or restart the session, then retry", err)
	}
	if limit := a.backgroundCap(); !a.Tasks.TryReserve(task, limit, false) {
		return "", fmt.Errorf("at most %d background subagents may run concurrently (current: %d); wait for one to finish or stop it with /subagents stop <id>",
			limit, a.Tasks.ActiveCount())
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.Tasks.AttachCancel(taskID, cancel)
	log := newWorkflowLog(task.TranscriptPath, query)

	go func() {
		defer cancel()
		// A bare detached goroutine: a panic here has no parent frame and
		// would take down the interactive session. Degrade it to an errored
		// task so the slot frees and the dock stops showing it running.
		defer func() {
			if r := recover(); r != nil {
				// Only if the run had not already reached a terminal state:
				// a panic in the post-finish notification must not overwrite a
				// real result with an error or notify the TUI a second time
				// (MarkDone overwrites Result even on a finished task).
				if snap, ok := a.Tasks.Get(taskID); ok && snap.Status == subagents.TaskRunning {
					err := panicToError("deep_research", r)
					a.finishResearchTask(task, "error: "+err.Error(), subagents.TaskErrored, true, log)
				}
			}
		}()
		emit := func(ev WorkflowPhase) {
			line := ev.Line()
			a.Tasks.AppendActivity(taskID, line)
			log.append(line)
		}
		res, err := t.research(ctx, query, breadth, cwd, emit)
		switch {
		case err != nil && ctx.Err() != nil:
			a.finishResearchTask(task, "deep research was stopped before it finished; no report was written.", subagents.TaskCanceled, true, log)
		case err != nil:
			a.finishResearchTask(task, "error: "+err.Error(), subagents.TaskErrored, true, log)
		default:
			log.append("\n" + res.summary())
			a.finishResearchTask(task, res.summary(), subagents.TaskCompleted, false, log)
		}
	}()

	return taskID, nil
}

// backgroundStartedMessage is the tool result a model sees after it starts a
// run itself (a tool call rather than the slash command).
func backgroundStartedMessage(taskID string) string {
	return fmt.Sprintf("deep research started as background task %s. It runs without blocking you: "+
		"progress shows in the subagents dock (/subagents to inspect or stop it), and the report is "+
		"delivered here when it finishes. Tell the user it has started, then stop — do not search, "+
		"fetch, or answer the question yourself.", taskID[:8])
}

// finishResearchTask records a terminal state and notifies the TUI exactly as
// a finished background subagent does.
func (a *AgentTool) finishResearchTask(task *subagents.Task, result string, status subagents.TaskStatus, errored bool, log *workflowLog) {
	a.Tasks.MarkDone(task.ID, status, result, errored, 0)
	log.append(fmt.Sprintf("\n[%s]", status))
	_, toolCalls := doneTokensAndCalls(a.Tasks, task.ID, 0)
	a.fireBackgroundDone(SubagentBackgroundDone{
		TaskID:       task.ID,
		AgentType:    task.AgentType,
		Result:       result,
		Errored:      errored,
		Duration:     time.Since(task.Started),
		ToolCalls:    toolCalls,
		NotifyOnDone: true,
	})
}

// workflowLog is the best-effort transcript behind the task's /subagents row:
// phase lines as they happen, then the final summary. Failures are swallowed
// (transcript loss must never affect the run), like the subagent transcripts.
type workflowLog struct{ path string }

func newWorkflowLog(path, query string) *workflowLog {
	l := &workflowLog{path: path}
	if path != "" {
		// The transcript dir is only resolved, not created, until some
		// subagent first writes there; the workflow log opens before any does.
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	l.append("# Deep research\n\n" + oneLine(query) + "\n")
	return l
}

func (l *workflowLog) append(line string) {
	if l == nil || l.path == "" {
		return
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
