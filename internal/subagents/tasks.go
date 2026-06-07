package subagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// TaskStatus is the lifecycle phase of a subagent run. The four
// terminal states are completed (clean TurnDone), errored (loop
// error or model returned an error message), canceled (parent
// invoked task.Cancel via /subagents stop), and iterCapped (hit
// MaxIterations without a final reply — surfaced as a soft error).
type TaskStatus int

const (
	TaskRunning TaskStatus = iota
	TaskCompleted
	TaskErrored
	TaskCanceled
	TaskIterCapped
)

// String renders the status as a short label suitable for `/subagents list`.
func (s TaskStatus) String() string {
	switch s {
	case TaskRunning:
		return "running"
	case TaskCompleted:
		return "done"
	case TaskErrored:
		return "errored"
	case TaskCanceled:
		return "canceled"
	case TaskIterCapped:
		return "iter-cap"
	default:
		return "unknown"
	}
}

// activityRingSize is the max number of progress entries we keep per
// task. Subagents can call dozens of tools across an investigation;
// the ring drops the oldest so the registry stays bounded and
// the picker's transcript view doesn't print megabytes of stale activity.
const activityRingSize = 64

// Task is one subagent run, foreground or background. The fields are
// snapshot-friendly (no live channels) so List() can return a copy
// without holding the registry lock while the caller renders.
type Task struct {
	ID             string
	AgentType      string
	Prompt         string
	Started        time.Time
	Finished       time.Time
	Status         TaskStatus
	Result         string
	Errored        bool
	Background     bool
	TokensUsed     int
	ToolCalls      int    // count of ToolStart events from the child
	Model          string // model the child ran on when task-routed; "" = inherited the parent's model
	TranscriptPath string
	// Branch / Worktree are set for dispatch write-subtasks that run in
	// their own git worktree+branch. Empty for ordinary (shared-cwd)
	// subagents. The integrate tool merges Branch into the integration
	// branch; the TUI shows it per task.
	Branch   string
	Worktree string
	// Base is the commit SHA the dispatch worktree branched from. The
	// session-exit sweep uses it to decide whether Branch ever gained
	// commits (base..HEAD) before reclaiming an empty worktree. Empty for
	// non-worktree subagents.
	Base string
	// BatchID groups the children of one dispatch call so the TUI can
	// render them together and the parent can refer to the batch. Empty
	// for standalone Agent dispatches.
	BatchID string
	// CtxTokens / CtxWindow track the subagent's live context usage,
	// updated each iteration from the child loop's ContextUsage event.
	// CtxWindow is 0 until the first update (and for child models whose
	// window couldn't be resolved); the dock shows a fill bar only when
	// CtxWindow > 0.
	CtxTokens int
	CtxWindow int
	// CanceledByUser is set when /subagents stop fires for this task.
	// Lets the runner distinguish "user explicitly stopped me" from
	// "parent turn was canceled" / "context deadline" in the outcome
	// message. Reading is safe under the registry lock; runChild
	// snapshots via Get().
	CanceledByUser bool
	Activities     []string
	cancel         context.CancelFunc
}

// Duration returns how long the task ran. For still-running tasks,
// it returns the time since Started.
func (t Task) Duration() time.Duration {
	if t.Finished.IsZero() {
		return time.Since(t.Started)
	}
	return t.Finished.Sub(t.Started)
}

// Registry tracks active and historical subagent tasks for the current
// session. Mutex-guarded so the agent goroutine, the TUI render path,
// and slash commands can all touch it safely. Snapshots returned by
// List() are deep-enough copies that callers can hold them indefinitely.
//
// waiters maps task id → list of channels signaled (closed) when the
// task transitions out of Running via MarkDone. The get_subagent_result
// tool registers a channel here when called with wait_seconds > 0,
// then blocks on it instead of polling. Channels are closed (not
// sent on) so multiple waiters per task work naturally — closing
// broadcasts to every receiver.
type Registry struct {
	mu      sync.RWMutex
	tasks   map[string]*Task
	waiters map[string][]chan struct{}
}

// NewRegistry returns an empty Registry ready for concurrent use.
func NewRegistry() *Registry {
	return &Registry{
		tasks:   map[string]*Task{},
		waiters: map[string][]chan struct{}{},
	}
}

// Add inserts a task. The ID is expected to be unique within the
// session; Add overwrites silently rather than failing so callers
// don't need to bother with conflict handling for short hex suffixes.
func (r *Registry) Add(t *Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.ID] = t
}

// Get returns a snapshot of the named task or (nil, false). The returned
// pointer references a copy, so callers can read its fields without
// holding the registry lock — but mutating it has no effect on the
// stored task.
func (r *Registry) Get(id string) (*Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	cp.Activities = append([]string(nil), t.Activities...)
	return &cp, true
}

// FindByPrefix is a typing-friendly accessor for /subagents stop: it
// returns the task whose ID starts with the given prefix. Returns
// (nil, false) when there's no match OR multiple matches (caller
// renders the disambiguation message). Matches Get's snapshot
// semantics.
func (r *Registry) FindByPrefix(prefix string) (*Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, false
	}
	var match *Task
	for id, t := range r.tasks {
		if strings.HasPrefix(id, prefix) {
			if match != nil {
				return nil, false
			}
			match = t
		}
	}
	if match == nil {
		return nil, false
	}
	cp := *match
	cp.Activities = append([]string(nil), match.Activities...)
	return &cp, true
}

// List returns a snapshot of every task, newest-first. Callers can
// retain the slice without locking.
func (r *Registry) List() []Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Task, 0, len(r.tasks))
	for _, t := range r.tasks {
		cp := *t
		cp.Activities = append([]string(nil), t.Activities...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// ActiveCount returns the number of tasks currently in TaskRunning.
// Used by the TUI to render a status-bar indicator and to enforce
// the background concurrency cap.
func (r *Registry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, t := range r.tasks {
		if t.Status == TaskRunning {
			n++
		}
	}
	return n
}

// ActiveForegroundCount returns the number of foreground tasks
// currently in TaskRunning. Used by AgentTool.Execute to enforce the
// foreground concurrency cap when the parent fans out multiple
// Agent calls in one assistant message (now parallel-safe).
func (r *Registry) ActiveForegroundCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, t := range r.tasks {
		if t.Status == TaskRunning && !t.Background {
			n++
		}
	}
	return n
}

// MarkDone updates the task's terminal state and signals every
// waiter blocked on WaitFor. Idempotent: calling MarkDone twice keeps
// the first finish time + status and overwrites only the
// Result/Errored fields if the caller passes different values. The
// cancel func is cleared so a later Cancel() call is a no-op rather
// than crashing.
//
// Waiters are signaled by *closing* their channels (not sending on
// them), which fans out to every receiver and makes the channels
// reusable on multiple `<-ch` receives. Both the running→terminal
// transition AND a no-op MarkDone on an already-finished task close
// any registered waiters — defensive against late waiters that
// registered just before MarkDone ran.
func (r *Registry) MarkDone(id string, status TaskStatus, result string, errored bool, tokensUsed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return
	}
	if t.Status == TaskRunning {
		t.Status = status
		t.Finished = time.Now()
	}
	t.Result = result
	t.Errored = errored
	if tokensUsed > 0 {
		t.TokensUsed = tokensUsed
	}
	t.cancel = nil
	// Signal anyone waiting on this task's completion. close() is the
	// broadcast primitive — every blocked receiver wakes up
	// simultaneously. Pop the waiters slice from the map so the
	// closed channels don't accumulate.
	for _, ch := range r.waiters[id] {
		close(ch)
	}
	delete(r.waiters, id)
}

// WaitFor returns a channel that is closed when the task transitions
// out of Running (via MarkDone). The channel is closed (never sent
// on) so multiple goroutines can wait simultaneously and unblock
// together. If the task is already terminal at call time, the
// returned channel is closed immediately so the caller's select
// case fires on the first iteration — no special-case needed at
// the call site.
//
// Returns a closed channel for unknown task ids too (rather than
// nil), so a typo doesn't deadlock the caller. The caller should
// re-read the task state after the wait returns to decide what
// "no longer running" actually means.
func (r *Registry) WaitFor(id string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.Status != TaskRunning {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	ch := make(chan struct{})
	r.waiters[id] = append(r.waiters[id], ch)
	return ch
}

// AppendActivity adds a short status string to the task's ring
// buffer. Oldest entries drop when the ring fills.
func (r *Registry) AppendActivity(id, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return
	}
	t.Activities = append(t.Activities, line)
	if len(t.Activities) > activityRingSize {
		t.Activities = t.Activities[len(t.Activities)-activityRingSize:]
	}
}

// Cancel invokes the task's cancel func (if any) so the underlying
// agent.Turn goroutine returns at the next context check. Marks
// CanceledByUser=true so the runner's outcome message can attribute
// the cancellation to /subagents stop rather than to a parent-turn
// cancellation or a context deadline. The MarkDone(TaskCanceled)
// follow-up is the responsibility of the goroutine itself when it
// observes the canceled context.
func (r *Registry) Cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.cancel == nil {
		return false
	}
	t.CanceledByUser = true
	t.cancel()
	t.cancel = nil
	return true
}

// CancelAll invokes every running task's cancel func, signaling all
// in-flight subagents — foreground AND detached background workers — to
// stop at their next context check. Used on session shutdown so background
// workers (which run on context.Background() to survive the parent turn)
// don't leak their goroutines and provider SSE streams past TUI exit.
// Returns the number of tasks signaled. Like Cancel, it does not mark the
// tasks done — each goroutine does that when it observes the canceled
// context; callers that need to wait for the drain can poll ActiveCount.
func (r *Registry) CancelAll() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.tasks {
		if t.cancel != nil {
			t.cancel()
			t.cancel = nil
			n++
		}
	}
	return n
}

// SetContextUsage records the subagent's current context size + window so
// the live dock can render a fill bar. Called each iteration from the
// runner as it forwards the child loop's ContextUsage event.
func (r *Registry) SetContextUsage(id string, tokens, window int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.CtxTokens = tokens
		t.CtxWindow = window
	}
}

// SetToolCalls records the final tool-call count for a task. Called
// by the runner just before MarkDone so the card / list can render
// an accurate ACTS value without inspecting the activities ring
// (which is bounded and may have dropped entries).
func (r *Registry) SetToolCalls(id string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.ToolCalls = n
	}
}

// AttachCancel records the cancel func so /subagents stop has a way to
// interrupt the child loop. Must be called before the goroutine
// starts; later AttachCancel calls overwrite.
func (r *Registry) AttachCancel(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.cancel = cancel
	}
}

// NewTaskID returns a short hex-encoded random ID suitable for a
// transcript filename and for prefix-matching by /subagents stop.
// 8 bytes → 16 hex chars; collisions within a single session are
// vanishingly unlikely.
func NewTaskID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
