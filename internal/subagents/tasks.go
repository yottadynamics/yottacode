package subagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
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
	ID         string
	AgentType  string
	Prompt     string
	Started    time.Time
	Finished   time.Time
	Status     TaskStatus
	Result     string
	Errored    bool
	Background bool
	// NotifyOnDone records that the spawner asked to be re-prompted with
	// this task's result when it finishes (the Agent tool's
	// notify_on_done). The TUI reads it to decide whether a background
	// completion should wake the model with the result, vs. stay silent
	// until fetched. Always false for foreground tasks.
	NotifyOnDone bool
	TokensUsed   int
	// Usage is the exact, provider-reported token tally accumulated across
	// the child's assistant turns (input/output/cache/reasoning). Unlike
	// TokensUsed — a single ~4-chars/token estimate stamped at MarkDone and
	// used only for the session budget backstop — Usage is updated live each
	// turn from the child loop's AssistantMessage event, and is the number
	// /usage folds into the session total. Zero (IsZero) for providers that
	// don't report usage; readers fall back to the TokensUsed estimate there.
	Usage           adapter.Usage
	ToolCalls       int    // count of ToolStart events from the child
	CompactionCount int    // number of times the child's own in-loop compaction fired (agent.ContextCompacted, Err == nil)
	Model           string // model the child ran on when task-routed; "" = inherited the parent's model
	TranscriptPath  string
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
	// Files is the write-scope a dispatch write worker declared ownership
	// of (dispatchTaskSpec.Files). Recorded on the registry record (not just
	// held locally in dispatchChild) so validateWritePartition can also
	// check a NEW dispatch call's claims against every OTHER still-running
	// dispatch task's claims — catching the case where two separate
	// dispatch calls, each internally non-overlapping, claim the same file
	// across calls. Empty for read-only and non-dispatch tasks.
	Files []string
	// Committing marks the window where a dispatch write-worker is staging and
	// committing its worktree to Branch. That work deliberately runs on a
	// cancellation-detached context so a just-finished worker still saves its
	// output — which means canceling the session does NOT stop it, and killing
	// the process mid-commit can abandon a half-written index (a stale
	// index.lock the user has to clear by hand in a worktree they never knew
	// existed). Session teardown reads this to keep waiting while a commit is
	// genuinely in flight, instead of abandoning it on a flat deadline.
	Committing bool
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
	// LastActivityAt is when the most recent activity was appended. Lets
	// the dock and get_subagent_result distinguish a subagent making steady
	// progress from one wedged on a single tool call: a long gap since the
	// last activity on a still-Running task is the stall signal. Zero until
	// the first activity.
	LastActivityAt time.Time
	// Historical marks a task rehydrated from a prior session (via Import):
	// a resolvable record of past work, not live this-session activity. The
	// session-token budget (TotalTokensUsed) skips these, and the TUI's
	// dropped-completion reconciliation skips them too, so resuming a session
	// neither starts with a drained budget nor re-banners/re-wakes on old
	// background results.
	Historical bool
	cancel     context.CancelFunc
}

// Duration returns how long the task ran. For still-running tasks,
// it returns the time since Started.
func (t Task) Duration() time.Duration {
	if t.Finished.IsZero() {
		return time.Since(t.Started)
	}
	return t.Finished.Sub(t.Started)
}

// UsageTokens returns the exact provider-reported total this subagent
// consumed (input + output + cache read + cache write), matching the
// "total tokens" basis /usage uses so every surface tells the same story.
// Returns 0 when the provider never reported usage (Usage.IsZero) — callers
// fall back to the ~4-char/token estimate then.
func (t Task) UsageTokens() int {
	u := t.Usage
	return int(u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens)
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

// TaskRecord is the JSON-serializable summary of a Task persisted in the
// session file so subagent task-ids survive a restart. The live cancel func,
// the activity ring, and live-context gauges are intentionally dropped — a
// rehydrated task is a historical record, not a re-attachable run. The
// Worktree/Base/Branch fields are kept so a startup sweep can reclaim a
// crashed session's empty dispatch worktrees.
type TaskRecord struct {
	ID              string        `json:"id"`
	AgentType       string        `json:"agent_type"`
	Prompt          string        `json:"prompt,omitempty"`
	Status          TaskStatus    `json:"status"`
	Result          string        `json:"result,omitempty"`
	Errored         bool          `json:"errored,omitempty"`
	Background      bool          `json:"background,omitempty"`
	NotifyOnDone    bool          `json:"notify_on_done,omitempty"`
	TranscriptPath  string        `json:"transcript_path,omitempty"`
	Started         time.Time     `json:"started,omitzero"`
	Finished        time.Time     `json:"finished,omitzero"`
	TokensUsed      int           `json:"tokens_used,omitempty"`
	Usage           adapter.Usage `json:"usage,omitzero"`
	ToolCalls       int           `json:"tool_calls,omitempty"`
	CompactionCount int           `json:"compaction_count,omitempty"`
	Model           string        `json:"model,omitempty"`
	Branch          string        `json:"branch,omitempty"`
	Worktree        string        `json:"worktree,omitempty"`
	Base            string        `json:"base,omitempty"`
	BatchID         string        `json:"batch_id,omitempty"`
}

// Export returns a serializable snapshot of every task for persistence in the
// session file, oldest-first. Taken under the read lock so it's safe to call
// alongside live registry mutation.
func (r *Registry) Export() []TaskRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TaskRecord, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, TaskRecord{
			ID: t.ID, AgentType: t.AgentType, Prompt: t.Prompt, Status: t.Status,
			Result: t.Result, Errored: t.Errored, Background: t.Background,
			NotifyOnDone: t.NotifyOnDone, TranscriptPath: t.TranscriptPath,
			Started: t.Started, Finished: t.Finished, TokensUsed: t.TokensUsed,
			Usage:           t.Usage,
			ToolCalls:       t.ToolCalls,
			CompactionCount: t.CompactionCount,
			Model:           t.Model, Branch: t.Branch,
			Worktree: t.Worktree, Base: t.Base, BatchID: t.BatchID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Import rehydrates persisted task records as HISTORICAL entries so a prior
// session's task-ids resolve (get_subagent_result, /subagents) after a
// restart. A record still Running when its session ended is unattachable —
// it's marked errored-orphaned so it reads as "didn't finish" rather than a
// phantom live task. Existing tasks with the same id are not overwritten, and
// the records don't count toward this session's concurrency cap (terminal) or
// token budget (historical). Typically called once at startup on an empty
// registry.
func (r *Registry) Import(records []TaskRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		if rec.ID == "" {
			continue
		}
		if _, exists := r.tasks[rec.ID]; exists {
			continue
		}
		status, result, errored := rec.Status, rec.Result, rec.Errored
		if status == TaskRunning {
			status, errored = TaskErrored, true
			if result == "" {
				result = "orphaned: the session that started this subagent ended while it was still running; re-run if you still need it"
			}
		}
		r.tasks[rec.ID] = &Task{
			ID: rec.ID, AgentType: rec.AgentType, Prompt: rec.Prompt, Status: status,
			Result: result, Errored: errored, Background: rec.Background,
			NotifyOnDone: rec.NotifyOnDone, TranscriptPath: rec.TranscriptPath,
			Started: rec.Started, Finished: rec.Finished, TokensUsed: rec.TokensUsed,
			Usage:           rec.Usage,
			ToolCalls:       rec.ToolCalls,
			CompactionCount: rec.CompactionCount,
			Model:           rec.Model, Branch: rec.Branch,
			Worktree: rec.Worktree, Base: rec.Base, BatchID: rec.BatchID,
			Historical: true,
		}
	}
}

// TryReserve atomically inserts t only if the relevant Running count is
// still below max, doing the count AND the insert under a single lock — so
// concurrent reservations (e.g. N parallel Agent calls in one assistant
// message) cannot each pass a separate check-then-Add and overshoot the
// cap. Returns false WITHOUT inserting when the class is already at/over max.
//
// countForegroundOnly selects the class: true counts only foreground Running
// tasks (the foreground cap); false counts ALL Running tasks (the background
// cap, which bounds total concurrency — matching the historical
// ActiveCount()-based check). t.Status should be TaskRunning.
func (r *Registry) TryReserve(t *Task, max int, countForegroundOnly bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.tasks {
		if e.Status != TaskRunning {
			continue
		}
		if countForegroundOnly && e.Background {
			continue
		}
		n++
	}
	if n >= max {
		return false
	}
	r.tasks[t.ID] = t
	return true
}

// TryReserveBatch is the all-or-nothing variant of TryReserve for a group of
// tasks that must be admitted together: it counts the Running class ONCE and
// inserts every task, or inserts none. Dispatch needs this because it spawns N
// workers from a single call — a per-task TryReserve loop could admit some and
// reject the rest, leaving a half-built batch whose worktrees are already on
// disk, and a check-then-Add (count here, insert in the spawned goroutines)
// reopens the very race TryReserve exists to close.
//
// countForegroundOnly selects the class, same as TryReserve. Every task's
// Status should be TaskRunning. Returns false WITHOUT inserting anything when
// the batch would push the class past max.
func (r *Registry) TryReserveBatch(tasks []*Task, max int, countForegroundOnly bool) bool {
	if len(tasks) == 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.tasks {
		if e.Status != TaskRunning {
			continue
		}
		if countForegroundOnly && e.Background {
			continue
		}
		n++
	}
	if n+len(tasks) > max {
		return false
	}
	for _, t := range tasks {
		r.tasks[t.ID] = t
	}
	return true
}

// BatchActiveCount returns how many tasks in the named dispatch batch are
// still TaskRunning. The TUI reads this to decide whether a batch's background
// completions are ready to wake the model: workers finish at staggered times,
// and waking once per worker would burn a turn each. Zero means the batch has
// fully drained (or the id is unknown). An empty batchID counts nothing —
// non-dispatch tasks carry no BatchID and must never be batch-gated.
func (r *Registry) BatchActiveCount(batchID string) int {
	if batchID == "" {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, t := range r.tasks {
		if t.Status == TaskRunning && t.BatchID == batchID {
			n++
		}
	}
	return n
}

// ActiveFileClaims returns the declared Files ownership of every currently
// TaskRunning task that has one, keyed by task ID. dispatch's
// validateWritePartition uses this to catch a collision BETWEEN two separate
// dispatch calls (e.g. two background batches issued in different turns),
// which a purely within-call overlap check can never see — each call's own
// task list can be internally disjoint while still claiming a file an
// earlier, still-running call already owns.
func (r *Registry) ActiveFileClaims() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]string)
	for id, t := range r.tasks {
		if t.Status == TaskRunning && len(t.Files) > 0 {
			out[id] = append([]string(nil), t.Files...)
		}
	}
	return out
}

// CancelWaiter removes ch from id's waiter list without closing it. Used by a
// caller that stops waiting before the task finishes (a timeout or ctx
// cancellation in get_subagent_result) so its channel doesn't sit in the
// registry for the rest of the task's run — WaitFor has no other way to
// deregister, and only MarkDone otherwise drains the list. A no-op if the
// task already went terminal (MarkDone already closed and popped the whole
// entry) or if ch isn't found.
func (r *Registry) CancelWaiter(id string, ch <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.waiters[id]
	for i, w := range list {
		if w == ch {
			r.waiters[id] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// WaiterCount returns how many channels are currently registered on id's
// waiter list. Exists mainly so callers/tests outside this package can
// confirm CancelWaiter actually deregisters (get_subagent_result's
// timeout/ctx-cancel paths) without reaching into unexported state.
func (r *Registry) WaiterCount(id string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.waiters[id])
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

// TotalTokensUsed sums the estimated TokensUsed across every task in the
// session (running tasks contribute 0 until MarkDone records their estimate).
// The Agent tool reads this to enforce a session-wide subagent token budget —
// a backstop against unbounded fan-out spend that the per-wave concurrency
// cap can't bound. O(n) over the task set, called once per spawn; n is small.
func (r *Registry) TotalTokensUsed() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, t := range r.tasks {
		if t.Historical {
			continue // prior-session spend doesn't deplete this session's budget
		}
		total += t.TokensUsed
	}
	return total
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
	t.LastActivityAt = time.Now()
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

// CancelBatch cancels every running member of one dispatch batch, leaving
// tasks outside it untouched. A batch is up to 8 workers, and stopping one by
// one via Cancel means finding and typing each id while the rest keep burning
// tokens. Returns the number signaled. Same contract as Cancel: the goroutines
// mark themselves done when they observe the canceled context. An empty
// batchID cancels nothing — batch-less tasks must never be swept up.
func (r *Registry) CancelBatch(batchID string) int {
	if batchID == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.tasks {
		if t.BatchID != batchID || t.cancel == nil {
			continue
		}
		t.CanceledByUser = true
		t.cancel()
		t.cancel = nil
		n++
	}
	return n
}

// SetCommitting marks (or clears) a task's commit-in-flight window. See
// Task.Committing — session teardown consults it before giving up on a drain.
func (r *Registry) SetCommitting(id string, committing bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.Committing = committing
	}
}

// CommittingCount returns how many running tasks are mid-commit. Session
// teardown polls this to extend its drain only while real work is at risk,
// rather than making every quit pay a longer worst-case wait.
func (r *Registry) CommittingCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, t := range r.tasks {
		if t.Status == TaskRunning && t.Committing {
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

// AddUsage accumulates one assistant turn's exact provider-reported token
// usage onto the task's running Usage total. Called each turn from the
// runner as it forwards the child loop's AssistantMessage event — mirroring
// what the main TUI loop does with session.AddUsage for the parent thread,
// so subagent spend is captured with the same fidelity. Nil-safe (a turn
// whose adapter reported no usage is a no-op) and a no-op for unknown ids.
func (r *Registry) AddUsage(id string, u *adapter.Usage) {
	if u == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.Usage.Add(u)
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

// IncrementCompactionCount records one successful firing of the child's own
// in-loop compaction (agent.ContextCompacted with Err == nil) — the runner
// calls this from its child-event loop alongside the existing activity-line
// rendering, so /usage can show a subagent's own compaction history instead
// of it being visible only as a transient activity string. A no-op for
// unknown ids.
func (r *Registry) IncrementCompactionCount(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.CompactionCount++
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
