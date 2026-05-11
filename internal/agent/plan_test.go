package agent

import (
	"sync"
	"testing"
)

func TestPlanStore_StartsEmpty(t *testing.T) {
	p := NewPlanStore()
	if got := p.Snapshot(); got != nil {
		t.Errorf("Snapshot on fresh store = %v, want nil", got)
	}
}

func TestPlanStore_ReplaceAndSnapshot(t *testing.T) {
	p := NewPlanStore()
	p.Replace([]Todo{
		{Content: "a", Status: TodoPending},
		{Content: "b", Status: TodoInProgress},
	})
	got := p.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(got))
	}
	if got[0].Content != "a" || got[1].Status != TodoInProgress {
		t.Errorf("Snapshot returned wrong items: %+v", got)
	}
}

func TestPlanStore_SnapshotIsDefensiveCopy(t *testing.T) {
	p := NewPlanStore()
	p.Replace([]Todo{{Content: "orig", Status: TodoPending}})
	snap := p.Snapshot()
	snap[0].Content = "mutated"
	again := p.Snapshot()
	if again[0].Content != "orig" {
		t.Errorf("internal state leaked: snapshot mutation propagated, got %q", again[0].Content)
	}
}

func TestPlanStore_ReplaceWithNilClears(t *testing.T) {
	p := NewPlanStore()
	p.Replace([]Todo{{Content: "a", Status: TodoPending}})
	p.Replace(nil)
	if got := p.Snapshot(); got != nil {
		t.Errorf("Snapshot after clear = %v, want nil", got)
	}
}

func TestPlanStore_ConcurrentReplaceSnapshot(t *testing.T) {
	p := NewPlanStore()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			p.Replace([]Todo{{Content: "x", Status: TodoPending}})
		}()
		go func() {
			defer wg.Done()
			_ = p.Snapshot()
		}()
	}
	wg.Wait()
	if got := p.Snapshot(); len(got) != 1 || got[0].Content != "x" {
		t.Errorf("final snapshot inconsistent: %+v", got)
	}
}
