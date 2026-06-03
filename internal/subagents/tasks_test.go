package subagents

import (
	"context"
	"sync"
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

func TestNewTaskID_NonEmpty(t *testing.T) {
	a, b := NewTaskID(), NewTaskID()
	if len(a) != 16 {
		t.Errorf("ID length = %d, want 16", len(a))
	}
	if a == b {
		t.Errorf("Two consecutive IDs collided: %q", a)
	}
}
