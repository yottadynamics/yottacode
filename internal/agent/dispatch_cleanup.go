package agent

import (
	"context"
	"os"
	"strings"

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// reclaimEmptyWorktree removes a dispatch worker's worktree (and its
// worktree-* branch, via worktree.Remove) when they hold nothing worth
// keeping: no commits beyond the dispatch base AND no uncommitted/untracked
// work. Returns true only when the worktree was actually removed.
//
// Emptiness must be AFFIRMATIVE on both probes — a git error (missing dir,
// index.lock held by a mid-commit worker, repo trouble) means "can't tell",
// and an uncertain worktree is kept, never deleted. Committed worktrees are
// kept for integrate; dirty ones are kept so a worker's partial output is
// never discarded (the P1 recovery posture).
//
// Callers pick the context deliberately: the per-worker path passes a
// cancellation-detached ctx (cleanup must survive the parent turn being
// canceled), while the session-exit sweep passes a bounded timeout ctx.
func reclaimEmptyWorktree(ctx context.Context, repoRoot, wtDir, base string) bool {
	if repoRoot == "" || wtDir == "" || base == "" {
		return false
	}
	// No commits beyond base?
	if out, err := gitOutput(ctx, wtDir, "rev-list", "-1", base+"..HEAD"); err != nil || strings.TrimSpace(out) != "" {
		return false
	}
	// Clean tree (nothing staged, modified, or untracked)?
	if out, err := gitOutput(ctx, wtDir, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		return false
	}
	// force=true: we just verified there's nothing to lose, and it makes the
	// removal robust against a stray transient file appearing mid-removal.
	return worktree.Remove(ctx, repoRoot, wtDir, true) == nil
}

// ReclaimEmptyDispatchWorktrees sweeps every dispatch worktree recorded in
// the task registry and reclaims the ones that hold nothing (no commits
// beyond their dispatch base, clean tree). Called at session teardown,
// after CancelAll + the bounded drain: workers that unwound in time already
// reclaimed their own worktree (their dir is gone — stat-skipped here); the
// sweep catches workers that were still stuck mid-run when the session
// died. Worktrees with commits awaiting integrate or with unsaved work are
// kept, same as the per-worker rule. Best-effort: every error is "keep",
// never fatal. Returns how many worktrees were removed.
func ReclaimEmptyDispatchWorktrees(ctx context.Context, tasks *subagents.Registry) int {
	if tasks == nil {
		return 0
	}
	n := 0
	for _, task := range tasks.List() {
		if task.Worktree == "" || task.Base == "" {
			continue
		}
		if _, err := os.Stat(task.Worktree); err != nil {
			continue // already reclaimed (or externally deleted)
		}
		// Resolve the main repo from inside the worktree itself, so the
		// sweep stays correct even if the session cwd moved to a different
		// repo after the dispatch ran.
		repoRoot, err := worktree.ResolveRepoRoot(ctx, task.Worktree)
		if err != nil {
			continue
		}
		if reclaimEmptyWorktree(ctx, repoRoot, task.Worktree, task.Base) {
			n++
		}
	}
	return n
}
