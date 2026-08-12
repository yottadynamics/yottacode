package agent

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

var dispatchReclaimMu sync.Mutex

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

	// Git worktree metadata is repo-global: removing a linked worktree updates
	// .git/worktrees and branch refs in the main repo. Dispatch workers finish
	// concurrently, so serialize the final prove-empty-and-remove sequence to
	// avoid one cleanup racing another through git's shared worktree state.
	dispatchReclaimMu.Lock()
	defer dispatchReclaimMu.Unlock()

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

// dispatchBranchPrefix is what `git worktree add -b` names a dispatch worker's
// branch: worktree.Branch("dispatch-<batch>-<i>"). Matching on it is how the
// orphan sweep tells dispatch's worktrees apart from ones the user made with
// `yottacode worktree add`, which must never be touched.
const dispatchBranchPrefix = worktree.BranchPrefix + "dispatch-"

// ReclaimOrphanDispatchWorktrees sweeps the repo's dispatch worktrees on disk
// and reclaims the ones holding nothing, WITHOUT needing a registry record.
//
// ReclaimEmptyDispatchWorktrees can only see worktrees the current session
// knows about — the ones it created, plus whatever the session import
// rehydrated. That misses the case the cleanup story most needs to cover: a
// session killed hard enough (SIGKILL, power loss, a crash before the session
// save) that its records never persisted, leaving worktrees no later session
// can attribute. Those used to accumulate forever. This walks `git worktree
// list` instead, so attribution comes from the branch name, not from memory.
//
// An orphan has no recorded dispatch base, so it's derived as the merge-base of
// the repo's HEAD and the worker's branch — the point the branch diverged.
// Commits past it are the worker's output and mean "keep", exactly as a
// recorded base would. Everything else is the shared conservative rule in
// reclaimEmptyWorktree: both probes must affirmatively say empty, any git error
// means keep. Locked worktrees are skipped outright — a lock is an explicit
// "don't touch this". Scoped to repoRoot; other repos are not this session's
// business. Best-effort, never fatal. Returns how many were removed.
func ReclaimOrphanDispatchWorktrees(ctx context.Context, repoRoot string) int {
	if repoRoot == "" {
		return 0
	}
	infos, err := worktree.List(ctx, repoRoot)
	if err != nil {
		return 0
	}
	n := 0
	for _, info := range infos {
		if info.Locked || !strings.HasPrefix(info.Branch, dispatchBranchPrefix) {
			continue
		}
		if _, err := os.Stat(info.Path); err != nil {
			continue // already gone; `git worktree prune` will drop the record
		}
		base, err := gitOutput(ctx, repoRoot, "merge-base", "HEAD", info.Branch)
		if err != nil {
			continue // can't establish a base → can't prove emptiness → keep
		}
		if reclaimEmptyWorktree(ctx, repoRoot, info.Path, strings.TrimSpace(base)) {
			n++
		}
	}
	return n
}
