package adapter

import (
	"sync"
	"testing"
	"time"
)

// TestHealthTracker_Disabled confirms that zero-value HealthOptions
// produces a tracker that never marks any candidate as degraded — the
// "off" config the user opts out with.
func TestHealthTracker_Disabled(t *testing.T) {
	h := newHealthTracker(HealthOptions{})
	for i := 0; i < 100; i++ {
		h.recordFailure("a")
	}
	if h.degraded("a") {
		t.Error("disabled tracker should never report degraded")
	}
	if h.enabled() {
		t.Error("zero-value HealthOptions should produce a disabled tracker")
	}
}

// TestHealthTracker_TripsOnThreshold confirms the basic counter
// behavior: N failures within the window mark the candidate degraded.
func TestHealthTracker_TripsOnThreshold(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 3})
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }

	if h.degraded("a") {
		t.Error("fresh candidate should not be degraded")
	}
	h.recordFailure("a")
	h.recordFailure("a")
	if h.degraded("a") {
		t.Error("two failures should be below threshold of 3")
	}
	h.recordFailure("a")
	if !h.degraded("a") {
		t.Error("three failures within window should be degraded")
	}
}

// TestHealthTracker_WindowExpiresFailures confirms older failures
// outside the sliding window stop counting toward degraded status.
func TestHealthTracker_WindowExpiresFailures(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: 60 * time.Second, Threshold: 2})
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }

	h.recordFailure("a")
	h.recordFailure("a")
	if !h.degraded("a") {
		t.Fatal("expected degraded after 2 failures")
	}

	// Advance past the window.
	now = now.Add(2 * time.Minute)
	if h.degraded("a") {
		t.Error("failures older than window should no longer count")
	}
}

// TestHealthTracker_SuccessClearsHistory confirms a recordSuccess
// fully resets the candidate's failure history rather than waiting for
// the window to slide it out.
func TestHealthTracker_SuccessClearsHistory(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 2})
	h.recordFailure("a")
	h.recordFailure("a")
	if !h.degraded("a") {
		t.Fatal("expected degraded after 2 failures")
	}
	h.recordSuccess("a")
	if h.degraded("a") {
		t.Error("recordSuccess should reset failure history")
	}
}

// TestHealthTracker_PerCandidate confirms tracking is keyed on label
// — one bad candidate must not poison others.
func TestHealthTracker_PerCandidate(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 2})
	h.recordFailure("bad")
	h.recordFailure("bad")
	if !h.degraded("bad") {
		t.Fatal("bad should be degraded")
	}
	if h.degraded("good") {
		t.Error("good should not be degraded; tracker leaked across candidates")
	}
}

// TestHealthTracker_OrderByHealthDemotes confirms the order helper
// stable-partitions: healthy first (in policy order), degraded last.
func TestHealthTracker_OrderByHealthDemotes(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 2})
	h.recordFailure("b")
	h.recordFailure("b")

	in := []Candidate{
		{Label: "a"},
		{Label: "b"}, // degraded
		{Label: "c"},
	}
	out := h.orderByHealth(in)
	want := []string{"a", "c", "b"}
	for i := range out {
		if out[i].Label != want[i] {
			t.Errorf("orderByHealth[%d].Label = %q, want %q (full: %v)",
				i, out[i].Label, want[i], labelsOf(out))
		}
	}
}

// TestHealthTracker_OrderByHealthAllDegraded confirms that when every
// candidate is degraded the original order is preserved — refusing to
// dispatch would be worse than tolerating one bad attempt that may now
// be recovered.
func TestHealthTracker_OrderByHealthAllDegraded(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 1})
	h.recordFailure("a")
	h.recordFailure("b")
	h.recordFailure("c")

	in := []Candidate{
		{Label: "a"},
		{Label: "b"},
		{Label: "c"},
	}
	out := h.orderByHealth(in)
	want := []string{"a", "b", "c"}
	for i := range out {
		if out[i].Label != want[i] {
			t.Errorf("orderByHealth[%d].Label = %q, want %q (all-degraded should preserve order)",
				i, out[i].Label, want[i])
		}
	}
}

// TestHealthTracker_OrderByHealthAllHealthy confirms passthrough when
// nothing is degraded.
func TestHealthTracker_OrderByHealthAllHealthy(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 3})
	in := []Candidate{
		{Label: "a"},
		{Label: "b"},
	}
	out := h.orderByHealth(in)
	for i := range out {
		if out[i].Label != in[i].Label {
			t.Errorf("orderByHealth changed order with no degraded candidates: got %v",
				labelsOf(out))
		}
	}
}

// TestHealthTracker_ConcurrentAccess catches data races on the
// internal map under -race. Each goroutine drives a different label
// so we exercise the per-candidate locking without contention noise.
func TestHealthTracker_ConcurrentAccess(t *testing.T) {
	h := newHealthTracker(HealthOptions{Window: time.Minute, Threshold: 5})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(label string) {
			defer wg.Done()
			for k := 0; k < 100; k++ {
				h.recordFailure(label)
				_ = h.degraded(label)
				if k%10 == 0 {
					h.recordSuccess(label)
				}
			}
		}(string(rune('a' + i)))
	}
	wg.Wait()
}
