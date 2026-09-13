package worktree

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// State describes the tree state of a worktree as it bears on
// cleanup. Each flag corresponds to one of the three things `git
// worktree remove` would refuse to silently throw away:
//
//	HasUncommitted  - tracked files modified or staged
//	HasUntracked    - new files not yet `git add`-ed
//	HasUnpushed     - committed but ahead of the configured upstream
type State struct {
	HasUncommitted bool
	HasUntracked   bool
	HasUnpushed    bool
}

// ParseCleanupAge accepts Go duration syntax plus day and week shorthand.
func ParseCleanupAge(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, fmt.Errorf("invalid cleanup age %q", value)
		}
		d := time.Duration(n * float64(24*time.Hour))
		if d <= 0 || float64(d) != n*float64(24*time.Hour) {
			return 0, fmt.Errorf("invalid cleanup age %q", value)
		}
		return d, nil
	}
	if strings.HasSuffix(value, "w") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "w"), 64)
		if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, fmt.Errorf("invalid cleanup age %q", value)
		}
		d := time.Duration(n * float64(7*24*time.Hour))
		if d <= 0 || float64(d) != n*float64(7*24*time.Hour) {
			return 0, fmt.Errorf("invalid cleanup age %q", value)
		}
		return d, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid cleanup age %q", value)
	}
	return d, nil
}

// CleanupOptions controls conservative cleanup of old managed worktrees.
type CleanupOptions struct {
	OlderThan time.Duration
	Force     bool
	DryRun    bool
	Current   string
}

// CleanupItem records one cleanup decision for reporting to a caller.
type CleanupItem struct {
	Name   string
	Path   string
	Age    time.Duration
	Action string
	Reason string
}

// Cleanup removes old, registered yottacode worktrees. It only considers
// exact managed paths, skips locked/current trees, and protects dirty trees
// unless Force is explicitly requested.
func Cleanup(ctx context.Context, repoRoot string, opts CleanupOptions) ([]CleanupItem, error) {
	if opts.OlderThan <= 0 {
		return nil, fmt.Errorf("worktree: cleanup age must be positive")
	}
	infos, err := List(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	items := make([]CleanupItem, 0)
	for _, info := range infos {
		name, ok := IsWorktreePath(repoRoot, info.Path)
		if !ok {
			continue
		}
		path := filepath.Clean(info.Path)
		item := CleanupItem{Name: name, Path: path}
		stat, statErr := os.Stat(path)
		if statErr != nil {
			item.Action, item.Reason = "skipped", fmt.Sprintf("cannot stat: %v", statErr)
		} else {
			item.Age = now.Sub(stat.ModTime())
			switch {
			case item.Age < opts.OlderThan:
				item.Action, item.Reason = "skipped", "not old enough"
			case info.Locked:
				item.Action, item.Reason = "skipped", "locked"
			case isWithinPath(opts.Current, path):
				item.Action, item.Reason = "skipped", "current worktree"
			default:
				state, stateErr := DetectState(ctx, path)
				switch {
				case stateErr != nil:
					item.Action, item.Reason = "skipped", fmt.Sprintf("cannot detect state: %v", stateErr)
				case !state.Clean() && !opts.Force:
					item.Action, item.Reason = "skipped", "dirty: "+strings.Join(state.Reasons(), ", ")
				case opts.DryRun:
					item.Action, item.Reason = "would remove", "eligible"
				default:
					if removeErr := Remove(ctx, repoRoot, path, opts.Force); removeErr != nil {
						item.Action, item.Reason = "error", removeErr.Error()
					} else {
						item.Action, item.Reason = "removed", "eligible"
					}
				}
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// isWithinPath reports whether current is inside path, including path itself.
func isWithinPath(current, path string) bool {
	if current == "" {
		return false
	}
	current, currentErr := filepath.EvalSymlinks(current)
	path, pathErr := filepath.EvalSymlinks(path)
	if currentErr != nil || pathErr != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(path), filepath.Clean(current))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// Clean reports whether nothing would be lost by removing the
// worktree.
func (s State) Clean() bool {
	return !s.HasUncommitted && !s.HasUntracked && !s.HasUnpushed
}

// Reasons returns a human-readable list of the dirty signals, for
// rendering in the keep-or-remove approval prompt.
func (s State) Reasons() []string {
	var r []string
	if s.HasUncommitted {
		r = append(r, "uncommitted changes")
	}
	if s.HasUntracked {
		r = append(r, "untracked files")
	}
	if s.HasUnpushed {
		r = append(r, "unpushed commits")
	}
	return r
}

// DetectState inspects worktreeDir and reports its cleanup-relevant
// state. The function does not modify anything.
//
// Unpushed detection is best-effort: if the branch has no upstream
// configured (the common case for a fresh worktree-* branch), we
// treat any commits ahead of the merge-base with `origin/HEAD` as
// unpushed. If origin/HEAD itself isn't resolvable (no remote,
// detached HEAD, fresh init), we report unpushed=false rather than
// failing — the caller's other signals will still flag a dirty tree
// when it matters.
func DetectState(ctx context.Context, worktreeDir string) (State, error) {
	var s State

	// Uncommitted (tracked-but-modified or staged) and untracked are
	// both surfaced by `git status --porcelain`. Lines starting with
	// "??" are untracked; anything else is uncommitted.
	status, err := gitOut(ctx, worktreeDir, "status", "--porcelain")
	if err != nil {
		return s, fmt.Errorf("worktree: detect state: %w", err)
	}
	for _, line := range strings.Split(status, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "?? ") {
			s.HasUntracked = true
		} else {
			s.HasUncommitted = true
		}
	}

	// Unpushed: try the configured upstream first; if that fails,
	// compare against origin/HEAD; if that fails too, give up
	// silently.
	if out, err := gitOut(ctx, worktreeDir, "rev-list", "--count", "@{u}..HEAD"); err == nil {
		if n := strings.TrimSpace(out); n != "" && n != "0" {
			s.HasUnpushed = true
		}
	} else if out, err := gitOut(ctx, worktreeDir, "rev-list", "--count", "origin/HEAD..HEAD"); err == nil {
		if n := strings.TrimSpace(out); n != "" && n != "0" {
			s.HasUnpushed = true
		}
	}

	return s, nil
}

// Remove deletes the worktree at worktreeDir and, if it was on a
// yottacode-managed branch (worktree-*), deletes that branch too.
// Pass force=true to override `git worktree remove`'s built-in
// dirty-tree check — callers should only do this after they have
// explicit user consent via the keep-or-remove prompt.
//
// repoRoot is the main repository (not the worktree) so the branch
// delete runs in a context that still has the branch around.
func Remove(ctx context.Context, repoRoot, worktreeDir string, force bool) error {
	// Capture the worktree's branch before removal so we can delete it
	// after. Branch lookup after `worktree remove` would fail.
	branch := ""
	if infos, err := List(ctx, repoRoot); err == nil {
		for _, w := range infos {
			if samePathRaw(w.Path, worktreeDir) {
				branch = w.Branch
				break
			}
		}
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreeDir)
	if _, err := gitOut(ctx, repoRoot, args...); err != nil {
		return fmt.Errorf("worktree: remove: %w", err)
	}

	// Only delete the branch if it follows our naming convention.
	// User-named branches are left alone.
	if strings.HasPrefix(branch, BranchPrefix) {
		if _, err := gitOut(ctx, repoRoot, "branch", "-D", branch); err != nil {
			// Non-fatal: the worktree is already gone; surface as a
			// soft warning by returning the error so callers can log it.
			return fmt.Errorf("worktree: removed but branch %s could not be deleted: %w", branch, err)
		}
	}

	// `git worktree remove` deletes the named worktree directory but
	// leaves the parent `<slug>/` dir behind. If that was the last
	// worktree for this repo, the slug dir is now "empty" (modulo our
	// `.origin` metadata file) — clean it up so removing your only
	// worktree actually returns the user-home tree to a pristine
	// state. A non-empty dir (another worktree under the same slug,
	// or unexpected files the user dropped there) is left alone.
	parent := filepath.Dir(filepath.Clean(worktreeDir))
	if slugDirIsEmpty(parent) {
		// Unlink .origin first so the rmdir succeeds; ignore both
		// failures — they're harmless leftovers, not correctness
		// issues.
		_ = os.Remove(OriginPath(parent))
		_ = os.Remove(parent)
	}
	return nil
}

// slugDirIsEmpty reports whether the slug dir contains nothing but
// (optionally) the `.origin` metadata file we drop. Used by Remove
// to decide when to clean up an empty slug dir.
func slugDirIsEmpty(slugDir string) bool {
	entries, err := os.ReadDir(slugDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != OriginFile {
			return false
		}
	}
	return true
}

// samePathRaw compares two paths after Clean. Used inside cleanup
// where we know both paths come from gitOut (already absolute) and
// don't need symlink resolution.
func samePathRaw(a, b string) bool {
	return cleanPath(a) == cleanPath(b)
}

func cleanPath(p string) string {
	return strings.TrimRight(strings.TrimSpace(p), "/")
}
