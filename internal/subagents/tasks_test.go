package subagents

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestNewTaskID_NonEmpty(t *testing.T) {
	a, b := NewTaskID(), NewTaskID()
	if len(a) != 16 {
		t.Errorf("ID length = %d, want 16", len(a))
	}
	if a == b {
		t.Errorf("Two consecutive IDs collided: %q", a)
	}
}
