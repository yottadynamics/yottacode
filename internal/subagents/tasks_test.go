package subagents

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestRegistry_CancelAll is the P3 shutdown regression: CancelAll must fire
// every running task's attached cancel func (so detached background workers
// don't leak past session exit) and be idempotent.
func TestRegistry_CancelAll(t *testing.T) {
	r := NewRegistry()
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	r.Add(&Task{ID: "aaaa1111", Status: TaskRunning})
	r.Add(&Task{ID: "bbbb2222", Status: TaskRunning})
	r.AttachCancel("aaaa1111", cancelA)
	r.AttachCancel("bbbb2222", cancelB)

	if n := r.CancelAll(); n != 2 {
		t.Fatalf("CancelAll signaled %d, want 2", n)
	}
	for name, ctx := range map[string]context.Context{"a": ctxA, "b": ctxB} {
		select {
		case <-ctx.Done():
		default:
			t.Errorf("task %s context was not canceled by CancelAll", name)
		}
	}
	// Idempotent: the cancel funcs are cleared, so a second call is a no-op.
	if n := r.CancelAll(); n != 0 {
		t.Errorf("second CancelAll signaled %d, want 0", n)
	}
}

func TestRegistry_AddGetList(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "aaaa1111", AgentType: "Explore", Started: time.Now(), Status: TaskRunning})
	r.Add(&Task{ID: "bbbb2222", AgentType: "Plan", Started: time.Now().Add(time.Second), Status: TaskCompleted})

	if got, ok := r.Get("aaaa1111"); !ok || got.AgentType != "Explore" {
		t.Errorf("Get(aaaa1111) = %v, %v", got, ok)
	}
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Newest-first sort
	if list[0].ID != "bbbb2222" {
		t.Errorf("List[0] = %s, want bbbb2222 (newest first)", list[0].ID)
	}
}

func TestRegistry_FindByPrefix(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "abc12345", Started: time.Now()})
	r.Add(&Task{ID: "def67890", Started: time.Now()})
	r.Add(&Task{ID: "abcd9999", Started: time.Now()})

	// Unique prefix
	if _, ok := r.FindByPrefix("def"); !ok {
		t.Errorf("FindByPrefix(def) should match def67890")
	}
	// Ambiguous prefix — registry returns not-found rather than guessing
	if _, ok := r.FindByPrefix("abc"); ok {
		t.Errorf("FindByPrefix(abc) should be ambiguous between abc12345 and abcd9999")
	}
	// Empty prefix
	if _, ok := r.FindByPrefix(""); ok {
		t.Errorf("FindByPrefix('') must not match")
	}
}

func TestRegistry_MarkDoneIdempotent(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "id1", Status: TaskRunning, Started: time.Now()})

	r.MarkDone("id1", TaskCompleted, "result-1", false, 100)
	got, _ := r.Get("id1")
	if got.Status != TaskCompleted {
		t.Fatalf("Status = %v, want completed", got.Status)
	}
	firstFinished := got.Finished

	// Second MarkDone with a different status — should preserve original
	// status + Finished, update only Result.
	r.MarkDone("id1", TaskErrored, "result-2", true, 200)
	got, _ = r.Get("id1")
	if got.Status != TaskCompleted {
		t.Errorf("Status changed on second MarkDone: %v", got.Status)
	}
	if !got.Finished.Equal(firstFinished) {
		t.Errorf("Finished changed on second MarkDone")
	}
	if got.Result != "result-2" {
		t.Errorf("Result = %q, want updated to result-2", got.Result)
	}
}

func TestRegistry_AppendActivityRingBound(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "id1", Started: time.Now()})
	for i := 0; i < activityRingSize*2; i++ {
		r.AppendActivity("id1", "tick")
	}
	got, _ := r.Get("id1")
	if len(got.Activities) != activityRingSize {
		t.Errorf("Activities len = %d, want %d", len(got.Activities), activityRingSize)
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	// Goal: exercise the mutex under `-race`. Many goroutines adding,
	// reading, and modifying simultaneously. Pass = no race detector
	// complaint and no panic.
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := NewTaskID()
			r.Add(&Task{ID: id, Started: time.Now(), Status: TaskRunning})
			r.AppendActivity(id, "first")
			r.MarkDone(id, TaskCompleted, "ok", false, 0)
			r.Get(id)
			_ = r.List()
			_ = r.ActiveCount()
		}(i)
	}
	wg.Wait()
}

// TestRegistry_TryReserve_AtomicUnderConcurrency is the cap-TOCTOU
// regression: many concurrent reservations against a small cap must grant
// EXACTLY cap (never overshoot), because the count + insert happen under one
// lock. Run under -race it also proves the path is data-race-free. Before
// TryReserve (when the cap was a separate ActiveCount check then Add) N
// racing reservations could all observe "under cap" and overshoot.
func TestRegistry_TryReserve_AtomicUnderConcurrency(t *testing.T) {
	const max = 8
	const attempts = 64
	r := NewRegistry()
	var wg sync.WaitGroup
	var granted atomic.Int32
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := &Task{ID: NewTaskID(), Started: time.Now(), Status: TaskRunning, Background: true}
			if r.TryReserve(task, max, false) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	// None of the reserved tasks ever complete, so exactly `max` succeed
	// and the rest are rejected — never more than the cap.
	if g := granted.Load(); g != max {
		t.Errorf("granted = %d, want exactly %d (cap overshot or undershot under concurrency)", g, max)
	}
	if ac := r.ActiveCount(); ac != max {
		t.Errorf("ActiveCount = %d, want %d", ac, max)
	}
}

// TestRegistry_TryReserve_ForegroundOnlyCountsForeground pins the class
// semantics: the foreground cap (countForegroundOnly=true) ignores background
// tasks, while the background/total cap counts everything.
func TestRegistry_TryReserve_ForegroundOnlyCountsForeground(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 8; i++ { // saturate with background load
		r.Add(&Task{ID: NewTaskID(), Status: TaskRunning, Background: true})
	}
	// Foreground cap of 2 is unaffected by the 8 background tasks.
	ok1 := r.TryReserve(&Task{ID: NewTaskID(), Status: TaskRunning}, 2, true)
	ok2 := r.TryReserve(&Task{ID: NewTaskID(), Status: TaskRunning}, 2, true)
	ok3 := r.TryReserve(&Task{ID: NewTaskID(), Status: TaskRunning}, 2, true)
	if !ok1 || !ok2 {
		t.Errorf("foreground reservations under cap must succeed despite background load; got %v %v", ok1, ok2)
	}
	if ok3 {
		t.Error("third foreground reservation must be rejected at foreground cap 2")
	}
	// Total/background cap counts everything: 8 bg + 2 fg = 10 ≥ 8 → reject.
	if r.TryReserve(&Task{ID: NewTaskID(), Status: TaskRunning, Background: true}, 8, false) {
		t.Error("background reservation must be rejected: 10 running already exceeds cap 8")
	}
}

// TestRegistry_TotalTokensUsed sums recorded estimates across tasks; a
// running task (no estimate yet) contributes 0.
func TestRegistry_TotalTokensUsed(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "a1", Status: TaskRunning})
	r.Add(&Task{ID: "b2", Status: TaskRunning})
	r.MarkDone("a1", TaskCompleted, "", false, 100)
	r.MarkDone("b2", TaskCompleted, "", false, 250)
	r.Add(&Task{ID: "c3", Status: TaskRunning}) // still running → 0
	if got := r.TotalTokensUsed(); got != 350 {
		t.Errorf("TotalTokensUsed = %d, want 350", got)
	}
}

// TestRegistry_ExportImportRoundTrip: a terminal task round-trips with its
// result (resolvable after a restart), a task still running at export
// rehydrates as an orphaned/errored historical record (not a phantom live
// task) while preserving worktree+base for the startup sweep, and rehydrated
// tasks count toward neither the concurrency cap nor the token budget.
func TestRegistry_ExportImportRoundTrip(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "donetask00000001", AgentType: "review", Background: true, Status: TaskRunning, Prompt: "find bugs"})
	r.MarkDone("donetask00000001", TaskCompleted, "the result", false, 1234)
	r.Add(&Task{ID: "runtask000000002", AgentType: "review", Background: true, Status: TaskRunning, Worktree: "/wt", Base: "abc"})

	records := r.Export()
	if len(records) != 2 {
		t.Fatalf("Export returned %d records, want 2", len(records))
	}

	r2 := NewRegistry()
	r2.Import(records)

	done, ok := r2.Get("donetask00000001")
	if !ok || done.Status != TaskCompleted || done.Result != "the result" {
		t.Errorf("completed task did not round-trip; got %+v ok=%v", done, ok)
	}
	orphan, ok := r2.Get("runtask000000002")
	if !ok {
		t.Fatal("a task running at export should rehydrate as a historical record")
	}
	if orphan.Status == TaskRunning {
		t.Error("a task running at export must NOT rehydrate as Running (it is unattachable)")
	}
	if !orphan.Errored || !strings.Contains(orphan.Result, "orphaned") {
		t.Errorf("orphan must be errored with an orphan note; got status=%v result=%q", orphan.Status, orphan.Result)
	}
	if orphan.Worktree != "/wt" || orphan.Base != "abc" {
		t.Errorf("orphan must preserve worktree+base for the startup sweep; got wt=%q base=%q", orphan.Worktree, orphan.Base)
	}
	if r2.ActiveCount() != 0 {
		t.Errorf("rehydrated tasks must not be Active; got %d", r2.ActiveCount())
	}
	if tok := r2.TotalTokensUsed(); tok != 0 {
		t.Errorf("historical (rehydrated) tokens must not deplete this session's budget; got %d", tok)
	}
}

// TestRegistry_ImportDoesNotOverwriteLiveTask: rehydration must never clobber
// a task already live in this session (defensive — Import normally runs once
// on an empty registry, but a same-id collision must keep the live entry).
func TestRegistry_ImportDoesNotOverwriteLiveTask(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "livetask00000001", AgentType: "review", Status: TaskRunning})
	r.Import([]TaskRecord{{ID: "livetask00000001", AgentType: "review", Status: TaskCompleted, Result: "stale"}})
	got, _ := r.Get("livetask00000001")
	if got.Status != TaskRunning || got.Result == "stale" {
		t.Errorf("Import must not overwrite a live task; got %+v", got)
	}
}

// TestRegistry_AddUsage: per-subagent usage accumulates across the child's
// turns from the exact provider-reported Usage, is nil-safe (a turn whose
// adapter reported nothing is a no-op), and no-ops on an unknown id. This is
// the capture the dock and /usage read — it mirrors how the main loop tallies
// the session.
func TestRegistry_AddUsage(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "sub1", Status: TaskRunning})

	r.AddUsage("sub1", &adapter.Usage{InputTokens: 100, OutputTokens: 40, CacheReadTokens: 500})
	r.AddUsage("sub1", &adapter.Usage{InputTokens: 30, OutputTokens: 10, ReasoningTokens: 5})
	r.AddUsage("sub1", nil)                               // no-usage turn: no-op
	r.AddUsage("ghost", &adapter.Usage{InputTokens: 999}) // unknown id: no panic, no leak

	got, ok := r.Get("sub1")
	if !ok {
		t.Fatal("task sub1 missing")
	}
	if got.Usage.InputTokens != 130 || got.Usage.OutputTokens != 50 {
		t.Errorf("accumulated in/out = %d/%d, want 130/50", got.Usage.InputTokens, got.Usage.OutputTokens)
	}
	if got.Usage.CacheReadTokens != 500 || got.Usage.ReasoningTokens != 5 {
		t.Errorf("accumulated cache/reasoning = %d/%d, want 500/5", got.Usage.CacheReadTokens, got.Usage.ReasoningTokens)
	}
	// UsageTokens is the inline-receipt basis: input + output + cache,
	// reasoning excluded. 130 + 50 + 500 = 680.
	if tok := got.UsageTokens(); tok != 680 {
		t.Errorf("UsageTokens = %d, want 680 (input+output+cache; reasoning excluded)", tok)
	}
}

// TestRegistry_ExportImportRoundTrip_Usage: a subagent's exact Usage survives
// the Export→session-file→Import cycle, so /usage still attributes subagent
// spend after a session resume (not just the ~4-char/token estimate).
func TestRegistry_ExportImportRoundTrip_Usage(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "usagetask0000001", AgentType: "Explore", Status: TaskRunning})
	r.AddUsage("usagetask0000001", &adapter.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 9000})
	r.MarkDone("usagetask0000001", TaskCompleted, "done", false, 111)

	r2 := NewRegistry()
	r2.Import(r.Export())

	got, ok := r2.Get("usagetask0000001")
	if !ok {
		t.Fatal("task did not round-trip")
	}
	if got.Usage.InputTokens != 1200 || got.Usage.OutputTokens != 340 || got.Usage.CacheReadTokens != 9000 {
		t.Errorf("Usage did not round-trip: got %+v", got.Usage)
	}
}

// TestRegistry_IncrementCompactionCount: repeated calls accumulate, and an
// unknown id is a no-op rather than a panic — mirroring SetToolCalls'
// nil-safety for an id the caller no longer holds a live task for.
func TestRegistry_IncrementCompactionCount(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "sub1", Status: TaskRunning})

	r.IncrementCompactionCount("sub1")
	r.IncrementCompactionCount("sub1")
	r.IncrementCompactionCount("ghost") // unknown id: no panic

	got, ok := r.Get("sub1")
	if !ok {
		t.Fatal("task sub1 missing")
	}
	if got.CompactionCount != 2 {
		t.Errorf("CompactionCount = %d, want 2", got.CompactionCount)
	}
}

// TestRegistry_ExportImportRoundTrip_CompactionCount: a subagent's own
// in-loop compaction count survives the Export→session-file→Import cycle,
// so /usage still shows it after a session resume.
func TestRegistry_ExportImportRoundTrip_CompactionCount(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "cctask0000000001", AgentType: "Explore", Status: TaskRunning})
	r.IncrementCompactionCount("cctask0000000001")
	r.IncrementCompactionCount("cctask0000000001")
	r.MarkDone("cctask0000000001", TaskCompleted, "done", false, 111)

	r2 := NewRegistry()
	r2.Import(r.Export())

	got, ok := r2.Get("cctask0000000001")
	if !ok {
		t.Fatal("task did not round-trip")
	}
	if got.CompactionCount != 2 {
		t.Errorf("CompactionCount did not round-trip: got %d, want 2", got.CompactionCount)
	}
}

func TestNewTaskID_NonEmpty(t *testing.T) {
	a, b := NewTaskID(), NewTaskID()
	if len(a) != 16 {
		t.Errorf("ID length = %d, want 16", len(a))
	}
	if a == b {
		t.Errorf("Two consecutive IDs collided: %q", a)
	}
}

// TestTryReserveBatch_AllOrNothing: a batch that fits is admitted whole; one
// that would breach the cap is rejected WITHOUT inserting any of its tasks.
// Dispatch depends on the all-or-nothing half — a partial admission would leave
// worktrees on disk for workers that were never allowed to run.
func TestTryReserveBatch_AllOrNothing(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "running-1", Status: TaskRunning, Background: true})

	over := []*Task{
		{ID: "over-1", Status: TaskRunning, Background: true},
		{ID: "over-2", Status: TaskRunning, Background: true},
		{ID: "over-3", Status: TaskRunning, Background: true},
	}
	if r.TryReserveBatch(over, 3, false) {
		t.Fatal("a batch of 3 against a cap of 3 with 1 already running must be rejected")
	}
	for _, tk := range over {
		if _, ok := r.Get(tk.ID); ok {
			t.Errorf("rejected batch inserted %s — reservation must be all-or-nothing", tk.ID)
		}
	}

	fits := []*Task{
		{ID: "fits-1", Status: TaskRunning, Background: true},
		{ID: "fits-2", Status: TaskRunning, Background: true},
	}
	if !r.TryReserveBatch(fits, 3, false) {
		t.Fatal("a batch of 2 against a cap of 3 with 1 running must be admitted")
	}
	if n := r.ActiveCount(); n != 3 {
		t.Errorf("ActiveCount = %d after admitting the batch, want 3", n)
	}

	// Terminal tasks free their slot, same as TryReserve.
	r.MarkDone("running-1", TaskCompleted, "", false, 0)
	if !r.TryReserveBatch([]*Task{{ID: "after-1", Status: TaskRunning, Background: true}}, 3, false) {
		t.Error("a finished task must free its slot for a later reservation")
	}
	// An empty batch is trivially admissible (nothing to insert).
	if !r.TryReserveBatch(nil, 0, false) {
		t.Error("an empty batch must be admitted")
	}
}

// TestBatchActiveCount: the TUI uses this to hold a dispatch batch's wakes
// until its LAST worker finishes, so it must count only running members of the
// named batch — and must never treat batch-less tasks as a batch.
func TestBatchActiveCount(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "b1-a", Status: TaskRunning, Background: true, BatchID: "batch-1"})
	r.Add(&Task{ID: "b1-b", Status: TaskRunning, Background: true, BatchID: "batch-1"})
	r.Add(&Task{ID: "b2-a", Status: TaskRunning, Background: true, BatchID: "batch-2"})
	r.Add(&Task{ID: "loner", Status: TaskRunning, Background: true})

	if n := r.BatchActiveCount("batch-1"); n != 2 {
		t.Errorf("BatchActiveCount(batch-1) = %d, want 2", n)
	}
	r.MarkDone("b1-a", TaskCompleted, "", false, 0)
	if n := r.BatchActiveCount("batch-1"); n != 1 {
		t.Errorf("after one worker finished: got %d, want 1", n)
	}
	r.MarkDone("b1-b", TaskCompleted, "", false, 0)
	if n := r.BatchActiveCount("batch-1"); n != 0 {
		t.Errorf("a fully drained batch must report 0, got %d", n)
	}
	if n := r.BatchActiveCount("batch-2"); n != 1 {
		t.Errorf("sibling batch must be unaffected, got %d", n)
	}
	// An empty id must never sweep up the batch-less tasks.
	if n := r.BatchActiveCount(""); n != 0 {
		t.Errorf(`BatchActiveCount("") = %d, want 0`, n)
	}
	if n := r.BatchActiveCount("nope"); n != 0 {
		t.Errorf("unknown batch = %d, want 0", n)
	}
}

// TestCancelBatch: cancels every running member of one dispatch batch and
// nothing else — sibling batches and batch-less tasks must be untouched, and
// an empty batch id must never sweep everything up.
func TestCancelBatch(t *testing.T) {
	r := NewRegistry()
	canceled := map[string]bool{}
	add := func(id, batch string) {
		r.Add(&Task{ID: id, Status: TaskRunning, Background: true, BatchID: batch})
		r.AttachCancel(id, func() { canceled[id] = true })
	}
	add("b1-a", "batch-1")
	add("b1-b", "batch-1")
	add("b2-a", "batch-2")
	add("loner", "")

	if n := r.CancelBatch("batch-1"); n != 2 {
		t.Fatalf("CancelBatch(batch-1) = %d, want 2", n)
	}
	if !canceled["b1-a"] || !canceled["b1-b"] {
		t.Error("both members of the batch must be canceled")
	}
	if canceled["b2-a"] || canceled["loner"] {
		t.Error("a sibling batch and a batch-less task must be untouched")
	}
	// Cancellation is attributed to the user, same as /subagents stop.
	if tk, _ := r.Get("b1-a"); tk == nil || !tk.CanceledByUser {
		t.Error("batch cancel must mark CanceledByUser so the outcome is attributed correctly")
	}
	// Idempotent: the cancel hooks are cleared, so a second call signals none.
	if n := r.CancelBatch("batch-1"); n != 0 {
		t.Errorf("second CancelBatch should signal 0, got %d", n)
	}
	// An empty id must never act as a wildcard.
	if n := r.CancelBatch(""); n != 0 {
		t.Errorf(`CancelBatch("") = %d, want 0 — it must not sweep up batch-less tasks`, n)
	}
	if canceled["loner"] {
		t.Error(`CancelBatch("") cancelled a batch-less task`)
	}
}

// TestCommittingCount tracks only RUNNING mid-commit tasks: session teardown
// uses it to decide whether to extend its drain, and a finished task must not
// hold the session open.
func TestCommittingCount(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "worker-1", Status: TaskRunning, Background: true})
	r.Add(&Task{ID: "worker-2", Status: TaskRunning, Background: true})

	if n := r.CommittingCount(); n != 0 {
		t.Errorf("nothing committing yet, got %d", n)
	}
	r.SetCommitting("worker-1", true)
	r.SetCommitting("worker-2", true)
	if n := r.CommittingCount(); n != 2 {
		t.Errorf("CommittingCount = %d, want 2", n)
	}
	r.SetCommitting("worker-1", false)
	if n := r.CommittingCount(); n != 1 {
		t.Errorf("after one cleared: got %d, want 1", n)
	}
	// A task that goes terminal stops counting even if the flag was never
	// cleared — that's what keeps a crashed worker from wedging the drain.
	r.MarkDone("worker-2", TaskCompleted, "", false, 0)
	if n := r.CommittingCount(); n != 0 {
		t.Errorf("a terminal task must not count as committing, got %d", n)
	}
	r.SetCommitting("nonexistent", true) // must not panic
}

// TestActiveFileClaims is the cross-call collision regression: it must
// report Files only for currently-Running tasks (dispatch's
// validateWritePartition uses this to catch a NEW dispatch call claiming a
// file an earlier, still-running call already owns), and must NOT report a
// terminal task's stale claim, which would otherwise permanently block that
// file from ever being claimed again.
func TestActiveFileClaims(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "running-1", Status: TaskRunning, Files: []string{"a.go", "b.go"}})
	r.Add(&Task{ID: "running-2", Status: TaskRunning}) // read-only: no Files
	done := &Task{ID: "done-1", Status: TaskRunning, Files: []string{"c.go"}}
	r.Add(done)
	r.MarkDone(done.ID, TaskCompleted, "ok", false, 0)

	claims := r.ActiveFileClaims()
	if len(claims) != 1 {
		t.Fatalf("ActiveFileClaims returned %d entries, want 1 (only running-1 has Files and is Running): %+v", len(claims), claims)
	}
	got := claims["running-1"]
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Errorf("running-1 claims = %v, want [a.go b.go]", got)
	}
	if _, ok := claims["done-1"]; ok {
		t.Error("a finished task's claim must not appear — it would permanently block that file")
	}
}

// TestCancelWaiter is the WaitFor-leak regression: a caller that stops
// waiting (timeout, ctx cancellation) before the task finishes must be able
// to deregister its own channel, or the registry accumulates one
// unreachable entry per abandoned wait for the task's remaining lifetime.
func TestCancelWaiter(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "t1", Status: TaskRunning})

	w1 := r.WaitFor("t1")
	w2 := r.WaitFor("t1")
	if n := len(r.waiters["t1"]); n != 2 {
		t.Fatalf("waiters[t1] = %d, want 2 after two WaitFor calls", n)
	}

	r.CancelWaiter("t1", w1)
	if n := len(r.waiters["t1"]); n != 1 {
		t.Fatalf("waiters[t1] = %d after canceling one waiter, want 1", n)
	}
	// w1 itself must not have been closed by CancelWaiter (only MarkDone
	// signals completion) — a caller that gave up must not see a false
	// "done" if it (incorrectly) selected on it again.
	select {
	case <-w1:
		t.Error("CancelWaiter must not close the channel, only deregister it")
	default:
	}

	// Canceling an unknown/already-removed channel is a no-op, not a panic.
	r.CancelWaiter("t1", w1)
	r.CancelWaiter("nonexistent-task", w1)
	if n := len(r.waiters["t1"]); n != 1 {
		t.Errorf("re-canceling an already-removed waiter changed the count: got %d, want 1", n)
	}

	// The remaining waiter still gets signaled normally.
	r.MarkDone("t1", TaskCompleted, "done", false, 0)
	select {
	case <-w2:
	default:
		t.Error("the un-canceled waiter should have been closed by MarkDone")
	}
}
