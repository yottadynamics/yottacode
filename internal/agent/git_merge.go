package agent

import (
	"context"
	"strings"
)

// gitMerge merges branch into the working tree at dir with a no-fast-forward,
// no-edit merge (so every task branch keeps a visible merge commit in the
// integration history).
//
// Return contract:
//   - clean merge: (nil, nil).
//   - conflict: (conflictedPaths, mergeErr). The merge is left in progress
//     in dir so the caller can resolve it in place and commit, or abort.
//     conflictedPaths is never empty in this case.
//   - other failure (bad branch, not a repo, …): (nil, err).
func gitMerge(ctx context.Context, dir, branch string) ([]string, error) {
	_, mergeErr := gitOutput(ctx, dir, "merge", "--no-ff", "--no-edit", branch)
	if mergeErr == nil {
		return nil, nil
	}
	// A merge conflict leaves unmerged entries we can enumerate; a hard
	// failure (unknown branch, dirty tree, not a repo) leaves none.
	if conflicts := gitConflictedFiles(ctx, dir); len(conflicts) > 0 {
		return conflicts, mergeErr
	}
	return nil, mergeErr
}

// gitConflictedFiles lists the unmerged (conflicted) paths in dir, or nil
// when there are none / the query fails. Used to classify a failed merge
// and to report which files the caller must resolve.
func gitConflictedFiles(ctx context.Context, dir string) []string {
	out, err := gitOutput(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	return nonEmptyLines(out)
}

// gitWorktreeDirty reports whether dir has any staged, unstaged, or
// untracked changes — i.e. whether there is anything to commit.
func gitWorktreeDirty(ctx context.Context, dir string) bool {
	out, err := gitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// nonEmptyLines splits s on newlines and drops blank lines, trimming each.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}
