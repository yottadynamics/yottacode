package adapter

import (
	"sync"
	"time"
)

// HealthOptions configures router-level health observation. Zero value
// means disabled (the router never marks a candidate as degraded). The
// router consults this tracker between policy ordering and dispatch:
// degraded candidates get demoted to the back of the order so the next
// request prefers candidates that have been working recently.
//
// Defaults documented in DefaultsTOML and applied by cli.BuildRouter:
//
//   - Window = 60 * time.Second  — sliding-window length
//   - Threshold = 3              — failures-in-window that mark degraded
//
// Set Threshold = 0 to disable observation entirely (every candidate
// stays "healthy" regardless of error history).
type HealthOptions struct {
	Window    time.Duration
	Threshold int
}

// healthTracker is the router-internal sliding-window failure counter.
// One tracker per MultiStreamer instance — health state does not
// persist across sessions, which is fine: TUI sessions are short and
// transient outages mostly clear within a single session anyway.
//
// Thread-safe: ChatStream goroutines record results concurrently when
// multiple turns are in flight (oneshot only ever runs one, but the
// TUI may spawn parallel work in future). Internal map access is
// guarded by mu; reads and writes both lock.
type healthTracker struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	window    time.Duration
	threshold int
	now       func() time.Time
}

func newHealthTracker(opts HealthOptions) *healthTracker {
	return &healthTracker{
		failures:  make(map[string][]time.Time),
		window:    opts.Window,
		threshold: opts.Threshold,
		now:       time.Now,
	}
}

// enabled reports whether the tracker will ever mark any candidate as
// degraded. Returns false when window or threshold is zero — both
// configurations should disable observation entirely.
func (h *healthTracker) enabled() bool {
	if h == nil {
		return false
	}
	return h.window > 0 && h.threshold > 0
}

// recordFailure adds a timestamp to the candidate's failure history.
// No-op when tracking is disabled.
func (h *healthTracker) recordFailure(label string) {
	if !h.enabled() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.failures[label] = append(h.pruneLocked(label, now), now)
}

// recordSuccess clears the candidate's failure history. A successful
// turn is the strongest signal that the candidate has recovered, so
// we reset rather than waiting for the window to slide.
func (h *healthTracker) recordSuccess(label string) {
	if !h.enabled() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.failures, label)
}

// degraded reports whether the candidate's failure count within the
// current window meets or exceeds the threshold.
func (h *healthTracker) degraded(label string) bool {
	if !h.enabled() {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	pruned := h.pruneLocked(label, now)
	h.failures[label] = pruned
	return len(pruned) >= h.threshold
}

// pruneLocked drops failure timestamps older than the window. Caller
// must hold h.mu. Returns the surviving slice (never nil — a fresh
// candidate has zero entries).
func (h *healthTracker) pruneLocked(label string, now time.Time) []time.Time {
	old := h.failures[label]
	if len(old) == 0 {
		return nil
	}
	cutoff := now.Add(-h.window)
	first := 0
	for first < len(old) && old[first].Before(cutoff) {
		first++
	}
	if first == 0 {
		return old
	}
	if first == len(old) {
		return nil
	}
	return append([]time.Time(nil), old[first:]...)
}

// orderByHealth stable-partitions candidates: healthy first (in the
// order received), degraded last (also in the order received). When
// every candidate is degraded the original order is preserved — the
// router still has to dispatch *something*, so refusing to retry would
// be worse than tolerating the bad attempt.
func (h *healthTracker) orderByHealth(candidates []Candidate) []Candidate {
	if !h.enabled() || len(candidates) == 0 {
		return candidates
	}
	healthy := make([]Candidate, 0, len(candidates))
	degraded := make([]Candidate, 0)
	for _, c := range candidates {
		if h.degraded(c.Label) {
			degraded = append(degraded, c)
		} else {
			healthy = append(healthy, c)
		}
	}
	if len(degraded) == 0 || len(healthy) == 0 {
		return candidates
	}
	return append(healthy, degraded...)
}
