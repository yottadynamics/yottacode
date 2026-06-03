package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// IntegrateToolName is the schema-visible name of the branch-assembly tool.
const IntegrateToolName = "integrate"

// IntegrateTool merges dispatch task branches into a single integration
// branch — the one branch a PR is opened from. It runs in a dedicated
// integration worktree so the user's working tree is never touched, merges
// each branch in order, and on a conflict stops with the conflicted files
// reported so the parent (or user) can resolve them and resume.
//
// Re-entrant: pass the same integration_branch back to continue after
// resolving a conflict or to add more branches.
type IntegrateTool struct {
	// Cwd is the session working dir, used to resolve the repo root.
	Cwd *CwdRef
	// Enabled gates the tool behind the `dispatch` experimental feature
	// (integrate and dispatch ship together). When false, Execute returns
	// a recoverable error string.
	Enabled bool
}

func (t *IntegrateTool) Name() string { return IntegrateToolName }

func (t *IntegrateTool) Description() string {
	var b strings.Builder
	b.WriteString("Merge dispatch task branches into one integration branch ready to open a PR from. ")
	b.WriteString("Pass the branches returned by a `dispatch` call. Merges run in a dedicated worktree (your working tree is untouched) in the given order. ")
	b.WriteString("On a merge conflict the tool STOPS and reports the conflicted files and the integration worktree path; resolve the conflict there (edit, then commit the merge), then call integrate again with the SAME integration_branch and the REMAINING branches to continue. ")
	b.WriteString("On full success it reports the integration branch to open a PR from.")
	return b.String()
}

func (t *IntegrateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"branches": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "The task branches to merge, in order (as returned by dispatch).",
			},
			"integration_branch": map[string]any{
				"type":        "string",
				"description": "Name of the integration branch. Omit on the first call (one is generated and returned); pass it back on subsequent calls to continue into the same branch.",
			},
			"base": map[string]any{
				"type":        "string",
				"description": "Base ref the integration branch starts from on first creation. Defaults to current HEAD.",
			},
		},
		"required": []string{"branches"},
	}
}

func (t *IntegrateTool) RequiresApproval(string) bool { return false }
func (t *IntegrateTool) ParallelSafe(string) bool     { return false }

func (t *IntegrateTool) PreviewCall(argsJSON string) string {
	a := parseIntegrateArgs(argsJSON)
	return fmt.Sprintf("integrate[%d branches]→%s", len(a.Branches), firstNonEmpty(a.IntegrationBranch, "(new)"))
}

type integrateArgs struct {
	Branches          []string `json:"branches"`
	IntegrationBranch string   `json:"integration_branch"`
	Base              string   `json:"base"`
}

func parseIntegrateArgs(argsJSON string) integrateArgs {
	var a integrateArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a
}

func (t *IntegrateTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if !t.Enabled {
		return "error: `integrate` is part of the experimental `dispatch` feature and is not enabled in this session. " +
			"Enable it with `--experimental dispatch`, `YOTTACODE_EXPERIMENTAL=dispatch`, or `[experimental]\\ndispatch = true` in config.toml.", nil
	}
	a := parseIntegrateArgs(argsJSON)
	if len(a.Branches) == 0 {
		return "error: integrate requires at least one branch (the branches returned by dispatch)", nil
	}

	repoRoot, err := worktree.ResolveRepoRoot(ctx, t.Cwd.Get())
	if err != nil {
		return "error: integrate requires a git repository: " + err.Error(), nil
	}

	integBranch := strings.TrimSpace(a.IntegrationBranch)
	if integBranch == "" {
		integBranch = "dispatch-integration-" + subagents.NewTaskID()[:8]
	}
	if err := worktree.ValidateName(integBranch); err != nil {
		return fmt.Sprintf("error: invalid integration_branch %q: %v", integBranch, err), nil
	}
	integDir := worktree.Dir(repoRoot, integBranch)

	base := strings.TrimSpace(a.Base)
	if base == "" {
		base = "HEAD"
	}

	if msg := t.ensureIntegrationWorktree(ctx, repoRoot, integBranch, integDir, base); msg != "" {
		return msg, nil
	}

	// Refuse to proceed if a previous merge is still unresolved in the
	// integration worktree — the caller must finish it first.
	if _, e := gitOutput(ctx, integDir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); e == nil {
		conflicts := gitConflictedFiles(ctx, integDir)
		return fmt.Sprintf("error: a merge is already in progress in the integration worktree %s. "+
			"Resolve the conflicted files (%s), `git add` them and commit the merge, then call integrate again with integration_branch=%q and the remaining branches.",
			integDir, strings.Join(conflicts, ", "), integBranch), nil
	}

	var merged, skipped []string
	for i, branch := range a.Branches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		// Skip branches that add nothing over the integration tip (e.g. a
		// write subtask that produced no changes).
		if !branchHasUniqueCommits(ctx, integDir, branch) {
			skipped = append(skipped, branch)
			continue
		}
		conflicts, mErr := gitMerge(ctx, integDir, branch)
		if mErr != nil {
			if len(conflicts) > 0 {
				remaining := a.Branches[i+1:]
				return t.conflictMessage(integBranch, integDir, branch, conflicts, remaining, merged), nil
			}
			return fmt.Sprintf("error: merging %s into %s failed: %v", branch, integBranch, mErr), nil
		}
		merged = append(merged, branch)
	}

	// Full success: the task branches' work is now captured in the
	// integration branch, so reclaim their throwaway worktrees + branches
	// instead of letting them accumulate forever (the old "prune with
	// git_worktree_prune" advice was a no-op against still-live worktrees).
	removedWts, keptWts := cleanupSourceWorktrees(ctx, repoRoot, merged, skipped)
	return t.successMessage(integBranch, integDir, merged, skipped, removedWts, keptWts), nil
}

// cleanupSourceWorktrees reclaims the per-task worktrees after a clean
// integration. Merged branches' worktrees (and the worktree-* branches
// themselves) are force-removed — their commits are safely reachable from
// the integration branch. Skipped branches contributed nothing to merge:
// the empty ones are removed too, but any worktree still holding
// uncommitted/untracked work is KEPT and its path returned, so a worker's
// partial output is never discarded. Best-effort — a removal failure is
// reported, never fatal (the integration already succeeded).
func cleanupSourceWorktrees(ctx context.Context, repoRoot string, merged, skipped []string) (removed, kept []string) {
	infos, err := worktree.List(ctx, repoRoot)
	if err != nil {
		return nil, nil
	}
	byBranch := map[string]string{}
	for _, w := range infos {
		if w.Branch != "" {
			byBranch[w.Branch] = w.Path
		}
	}
	for _, br := range merged {
		if dir, ok := byBranch[br]; ok {
			if err := worktree.Remove(ctx, repoRoot, dir, true); err == nil {
				removed = append(removed, br)
			} else {
				kept = append(kept, dir)
			}
		}
	}
	for _, br := range skipped {
		dir, ok := byBranch[br]
		if !ok {
			continue
		}
		// Only remove an empty branch's worktree when it holds no unsaved
		// work; otherwise keep it so the user can recover what's there.
		if st, e := worktree.DetectState(ctx, dir); e == nil && !st.HasUncommitted && !st.HasUntracked {
			if err := worktree.Remove(ctx, repoRoot, dir, true); err == nil {
				removed = append(removed, br)
				continue
			}
		}
		kept = append(kept, dir)
	}
	return removed, kept
}

// ensureIntegrationWorktree makes sure integDir is a worktree checked out on
// integBranch, creating the branch and/or worktree as needed. Returns "" on
// success or a recoverable error message.
func (t *IntegrateTool) ensureIntegrationWorktree(ctx context.Context, repoRoot, integBranch, integDir, base string) string {
	if isDir(integDir) {
		// The directory exists — but before piling merges into it, verify it
		// really is OUR integration worktree: a registered worktree of this
		// repo, checked out on integBranch. Reusing any directory that merely
		// sits at this path (a stray dir, a worktree on the wrong branch, a
		// detached HEAD) would merge branches into the wrong place silently.
		branch, err := gitOutput(ctx, integDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return fmt.Sprintf("error: %s exists but is not a usable git worktree (%v); remove it or pass a different integration_branch", integDir, err)
		}
		if got := strings.TrimSpace(branch); got != integBranch {
			return fmt.Sprintf("error: %s is checked out on %q, not the integration branch %q; remove it or pass a different integration_branch", integDir, got, integBranch)
		}
		if !isRegisteredWorktree(ctx, repoRoot, integDir) {
			return fmt.Sprintf("error: %s is on branch %q but is not a registered worktree of this repo; remove it or pass a different integration_branch", integDir, integBranch)
		}
		return "" // verified — reuse the existing integration worktree
	}
	branchExists := false
	if _, e := gitOutput(ctx, repoRoot, "rev-parse", "--verify", "refs/heads/"+integBranch); e == nil {
		branchExists = true
	}
	var err error
	if branchExists {
		// Branch exists but its worktree is gone — re-attach a worktree.
		_, err = gitOutput(ctx, repoRoot, "worktree", "add", integDir, integBranch)
	} else {
		_, err = gitOutput(ctx, repoRoot, "worktree", "add", "-b", integBranch, integDir, base)
	}
	if err != nil {
		return fmt.Sprintf("error: could not create integration worktree for %q: %v", integBranch, err)
	}
	return ""
}

func (t *IntegrateTool) conflictMessage(integBranch, integDir, branch string, conflicts, remaining, merged []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CONFLICT integrating %s into %s.\n", branch, integBranch)
	if len(merged) > 0 {
		fmt.Fprintf(&b, "Already merged cleanly: %s.\n", strings.Join(merged, ", "))
	}
	fmt.Fprintf(&b, "Conflicted files (in %s):\n", integDir)
	for _, f := range conflicts {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	b.WriteString("\nTo continue: open the conflicted files in that worktree, resolve the markers, `git add` them, and commit the merge. ")
	fmt.Fprintf(&b, "Then call integrate again with integration_branch=%q", integBranch)
	if len(remaining) > 0 {
		fmt.Fprintf(&b, " and branches=[%s]", strings.Join(remaining, ", "))
	} else {
		b.WriteString(" and branches=[] to finalize")
	}
	b.WriteString(".\n")
	// Alternative: skip this branch for now rather than resolving in place.
	fmt.Fprintf(&b, "Or to drop %s from this round, run `git merge --abort` in %s and call integrate again with integration_branch=%q",
		branch, integDir, integBranch)
	if len(remaining) > 0 {
		fmt.Fprintf(&b, " and branches=[%s]", strings.Join(remaining, ", "))
	}
	fmt.Fprintf(&b, " (re-include %s in a later call once it's fixed).\n", branch)
	return b.String()
}

func (t *IntegrateTool) successMessage(integBranch, integDir string, merged, skipped, removedWts, keptWts []string) string {
	var b strings.Builder
	if len(merged) == 0 {
		fmt.Fprintf(&b, "Integration branch %s is up to date — nothing new to merge", integBranch)
		if len(skipped) > 0 {
			fmt.Fprintf(&b, " (skipped empty branches: %s)", strings.Join(skipped, ", "))
		}
		b.WriteString(".\n")
	} else {
		fmt.Fprintf(&b, "Integrated %d branch(es) into %s cleanly: %s.\n", len(merged), integBranch, strings.Join(merged, ", "))
		if len(skipped) > 0 {
			fmt.Fprintf(&b, "Skipped empty branches: %s.\n", strings.Join(skipped, ", "))
		}
	}
	fmt.Fprintf(&b, "Integration worktree: %s\n", integDir)
	fmt.Fprintf(&b, "Open a PR from branch %q (e.g. push it and use /create-pr).\n", integBranch)
	if len(removedWts) > 0 {
		fmt.Fprintf(&b, "Reclaimed %d task worktree(s) + branch(es) now that their work is integrated.\n", len(removedWts))
	}
	if len(keptWts) > 0 {
		fmt.Fprintf(&b, "Kept %d task worktree(s) that still hold uncommitted work — recover or discard manually: %s.\n", len(keptWts), strings.Join(keptWts, ", "))
	}
	return b.String()
}

// branchHasUniqueCommits reports whether branch has any commit not already
// reachable from the integration tip (HEAD of integDir) — i.e. whether
// merging it would add anything.
func branchHasUniqueCommits(ctx context.Context, integDir, branch string) bool {
	out, err := gitOutput(ctx, integDir, "rev-list", "--count", "HEAD.."+branch)
	if err != nil {
		// Unknown branch or other error — let the merge attempt surface it.
		return true
	}
	return strings.TrimSpace(out) != "0"
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// isRegisteredWorktree reports whether dir is a worktree of repoRoot
// according to `git worktree list` — the guard against reusing a directory
// that sits at the integration path but isn't actually one of this repo's
// worktrees.
func isRegisteredWorktree(ctx context.Context, repoRoot, dir string) bool {
	infos, err := worktree.List(ctx, repoRoot)
	if err != nil {
		return false
	}
	for _, w := range infos {
		if worktree.SamePath(w.Path, dir) {
			return true
		}
	}
	return false
}
