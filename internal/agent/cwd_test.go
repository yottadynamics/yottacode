package agent

import (
	"sync"
	"testing"
)

func TestCwdRefRoundtrip(t *testing.T) {
	r := NewCwdRef("/initial")
	if got := r.Get(); got != "/initial" {
		t.Fatalf("Get: got %q, want /initial", got)
	}
	r.Set("/new")
	if got := r.Get(); got != "/new" {
		t.Fatalf("Get after Set: got %q, want /new", got)
	}
}

func TestCwdRefNilSafe(t *testing.T) {
	var r *CwdRef
	if got := r.Get(); got != "" {
		t.Fatalf("nil.Get: got %q, want empty", got)
	}
	// Should not panic.
	r.Set("/whatever")
}

func TestCwdRefConcurrent(t *testing.T) {
	// Belt-and-braces race-cleanness: parallel Sets and Gets should
	// not race. Run with `go test -race`.
	r := NewCwdRef("/start")
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Set("/a")
		}()
		go func() {
			defer wg.Done()
			_ = r.Get()
		}()
	}
	wg.Wait()
}
