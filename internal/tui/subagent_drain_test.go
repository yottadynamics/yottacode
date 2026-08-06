package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestDrainSubagents_QuietWhenNothingCommitting: the common quit path — a
// canceled worker that unwinds within the grace window must not print anything
// and must not push the session into the extended wait.
func TestDrainSubagents_QuietWhenNothingCommitting(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "worker-1", Status: subagents.TaskRunning, Background: true})
	go func() {
		time.Sleep(20 * time.Millisecond)
		reg.MarkDone("worker-1", subagents.TaskCanceled, "", false, 0)
	}()

	var out strings.Builder
	start := time.Now()
	drainSubagents(reg, 2*time.Second, 2*time.Second, time.Millisecond, &out)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("drain took %s — it should return as soon as the worker unwinds", elapsed)
	}
	if out.String() != "" {
		t.Errorf("a clean quit must stay silent, printed: %q", out.String())
	}
}

// TestDrainSubagents_WaitsOutMidCommitWorker is the regression for the
// abandoned-commit gap: a dispatch worker's auto-commit runs on a
// cancellation-detached context, so teardown can't stop it — only outrun it.
// On a flat 3s deadline a `git add -A` plus a slow pre-commit hook lost that
// race, killing git mid-write and leaving a stale index.lock. The worker must
// be waited out past the grace window instead, with the pause explained.
func TestDrainSubagents_WaitsOutMidCommitWorker(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "committer000001", Status: subagents.TaskRunning, Background: true})
	reg.SetCommitting("committer000001", true)

	// Finishes well after the grace window, well within the commit ceiling.
	go func() {
		time.Sleep(150 * time.Millisecond)
		reg.SetCommitting("committer000001", false)
		reg.MarkDone("committer000001", subagents.TaskCompleted, "committed", false, 0)
	}()

	var out strings.Builder
	start := time.Now()
	drainSubagents(reg, 20*time.Millisecond, 5*time.Second, time.Millisecond, &out)
	elapsed := time.Since(start)

	if elapsed < 100*time.Millisecond {
		t.Errorf("drain returned after %s — it abandoned a worker that was mid-commit", elapsed)
	}
	if n := reg.CommittingCount(); n != 0 {
		t.Errorf("drain returned with %d workers still committing", n)
	}
	if !strings.Contains(out.String(), "finish committing") {
		t.Errorf("the extended wait must explain itself (Bubbletea already released the terminal); got: %q", out.String())
	}
	if strings.Contains(out.String(), "gave up") {
		t.Errorf("a worker that finished in time must not report as abandoned; got: %q", out.String())
	}
}

// TestDrainSubagents_CommitCeilingIsBounded: a wedged pre-commit hook must not
// hold the session open forever — the extended wait is bounded, and giving up
// tells the user how to recover.
func TestDrainSubagents_CommitCeilingIsBounded(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "wedged000000001", Status: subagents.TaskRunning, Background: true})
	reg.SetCommitting("wedged000000001", true) // never clears

	var out strings.Builder
	start := time.Now()
	drainSubagents(reg, 10*time.Millisecond, 60*time.Millisecond, time.Millisecond, &out)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a wedged commit held the drain for %s — the ceiling must bound it", elapsed)
	}
	if !strings.Contains(out.String(), "gave up") || !strings.Contains(out.String(), "index.lock") {
		t.Errorf("giving up must name the recovery step; got: %q", out.String())
	}
}
