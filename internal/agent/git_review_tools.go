package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The tools in this file are the workflow layer of the v0.3.0 "git tool
// refresh": cheap, read-only review surfaces (diffstat, staged/unstaged
// diffs, commit ranges, branch relationship) so the model stops
// composing raw `git diff` flag combinations per call, plus two
// structured commit helpers (amend, fixup) whose approval copy says
// what they rewrite. The read-only six auto-execute and are
// parallel-safe; the two commit helpers always prompt.

type GitDiffStatTool struct{ Cwd *CwdRef }

func (t *GitDiffStatTool) Name() string { return "git_diff_stat" }
func (t *GitDiffStatTool) Description() string {
	return "Compact diffstat (files + line counts, no hunks) for the working tree or a ref/range. " +
		"The cheap first-pass review surface — call this before pulling full diffs."
}
func (t *GitDiffStatTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base":  map[string]any{"type": "string", "description": "Base revision (optional; empty = working tree vs index/HEAD)"},
		"head":  map[string]any{"type": "string", "description": "Head revision (optional; requires base)"},
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional path filter"},
	}}
}
func (t *GitDiffStatTool) RequiresApproval(string) bool { return false }
func (t *GitDiffStatTool) ParallelSafe(string) bool     { return true }
func (t *GitDiffStatTool) PreviewCall(argsJSON string) string {
	var a struct{ Base, Head string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	switch {
	case a.Base != "" && a.Head != "":
		return fmt.Sprintf("git_diff_stat(%s..%s)", a.Base, a.Head)
	case a.Base != "":
		return fmt.Sprintf("git_diff_stat(%s)", a.Base)
	}
	return "git_diff_stat(working tree)"
}
func (t *GitDiffStatTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Base, Head string
		Paths      []string
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_diff_stat: invalid args: %w", err)
	}
	args := []string{"diff", "--stat"}
	switch {
	case strings.TrimSpace(a.Base) != "" && strings.TrimSpace(a.Head) != "":
		args = append(args, a.Base, a.Head)
	case strings.TrimSpace(a.Base) != "":
		args = append(args, a.Base)
	case strings.TrimSpace(a.Head) != "":
		return "", errors.New("git_diff_stat: head requires base")
	}
	if len(a.Paths) > 0 {
		args = append(args, "--")
		args = append(args, a.Paths...)
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), args...)
	if err != nil {
		return "", fmt.Errorf("git_diff_stat: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "(no changes)\n", nil
	}
	return out, nil
}

type GitDiffStagedTool struct{ Cwd *CwdRef }

func (t *GitDiffStagedTool) Name() string { return "git_diff_staged" }
func (t *GitDiffStagedTool) Description() string {
	return "Diff of the STAGED changes (what `git commit` would record right now). " +
		"Equivalent to `git diff --cached` — no flag guessing needed."
}
func (t *GitDiffStagedTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional path filter"},
	}}
}
func (t *GitDiffStagedTool) RequiresApproval(string) bool { return false }
func (t *GitDiffStagedTool) ParallelSafe(string) bool     { return true }
func (t *GitDiffStagedTool) PreviewCall(string) string    { return "git_diff_staged()" }
func (t *GitDiffStagedTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Paths []string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_diff_staged: invalid args: %w", err)
	}
	args := []string{"diff", "--cached"}
	if len(a.Paths) > 0 {
		args = append(args, "--")
		args = append(args, a.Paths...)
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), args...)
	if err != nil {
		return "", fmt.Errorf("git_diff_staged: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "(nothing staged)\n", nil
	}
	return out, nil
}

type GitDiffUnstagedTool struct{ Cwd *CwdRef }

func (t *GitDiffUnstagedTool) Name() string { return "git_diff_unstaged" }
func (t *GitDiffUnstagedTool) Description() string {
	return "Diff of the UNSTAGED changes (tracked edits not yet `git add`-ed). " +
		"Untracked files never appear in any diff — list those via list_git_changed_files."
}
func (t *GitDiffUnstagedTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional path filter"},
	}}
}
func (t *GitDiffUnstagedTool) RequiresApproval(string) bool { return false }
func (t *GitDiffUnstagedTool) ParallelSafe(string) bool     { return true }
func (t *GitDiffUnstagedTool) PreviewCall(string) string    { return "git_diff_unstaged()" }
func (t *GitDiffUnstagedTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Paths []string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_diff_unstaged: invalid args: %w", err)
	}
	args := []string{"diff"}
	if len(a.Paths) > 0 {
		args = append(args, "--")
		args = append(args, a.Paths...)
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), args...)
	if err != nil {
		return "", fmt.Errorf("git_diff_unstaged: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "(no unstaged changes)\n", nil
	}
	return out, nil
}

type GitCommitsBetweenTool struct{ Cwd *CwdRef }

func (t *GitCommitsBetweenTool) Name() string { return "git_commits_between" }
func (t *GitCommitsBetweenTool) Description() string {
	return "One-line commit summaries in base..head (commits reachable from head but not base), " +
		"newest first. The review-and-PR-explanation range view."
}
func (t *GitCommitsBetweenTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base":  map[string]any{"type": "string", "description": "Base ref (exclusive)"},
		"head":  map[string]any{"type": "string", "description": "Head ref (default HEAD)"},
		"limit": map[string]any{"type": "integer", "description": "Max commits (default 20)"},
	}, "required": []string{"base"}}
}
func (t *GitCommitsBetweenTool) RequiresApproval(string) bool { return false }
func (t *GitCommitsBetweenTool) ParallelSafe(string) bool     { return true }
func (t *GitCommitsBetweenTool) PreviewCall(argsJSON string) string {
	var a struct{ Base, Head string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Head) == "" {
		a.Head = "HEAD"
	}
	return fmt.Sprintf("git_commits_between(%s..%s)", a.Base, a.Head)
}
func (t *GitCommitsBetweenTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Base, Head string
		Limit      int
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_commits_between: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Base) == "" {
		return "", errors.New("git_commits_between: base is required")
	}
	if strings.TrimSpace(a.Head) == "" {
		a.Head = "HEAD"
	}
	if a.Limit <= 0 {
		a.Limit = 20
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), "log", "--oneline",
		"-n", fmt.Sprintf("%d", a.Limit), a.Base+".."+a.Head)
	if err != nil {
		return "", fmt.Errorf("git_commits_between: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "(no commits in range)\n", nil
	}
	return out, nil
}

type GitBranchAheadBehindTool struct{ Cwd *CwdRef }

func (t *GitBranchAheadBehindTool) Name() string { return "git_branch_ahead_behind" }
func (t *GitBranchAheadBehindTool) Description() string {
	return "Branch relationship in one call: ahead/behind counts between head and base, plus their " +
		"merge base. Replaces composing merge-base + rev-list by hand."
}
func (t *GitBranchAheadBehindTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base": map[string]any{"type": "string", "description": "Base ref to compare against"},
		"head": map[string]any{"type": "string", "description": "Head ref (default HEAD)"},
	}, "required": []string{"base"}}
}
func (t *GitBranchAheadBehindTool) RequiresApproval(string) bool { return false }
func (t *GitBranchAheadBehindTool) ParallelSafe(string) bool     { return true }
func (t *GitBranchAheadBehindTool) PreviewCall(argsJSON string) string {
	var a struct{ Base, Head string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Head) == "" {
		a.Head = "HEAD"
	}
	return fmt.Sprintf("git_branch_ahead_behind(%s vs %s)", a.Head, a.Base)
}
func (t *GitBranchAheadBehindTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Base, Head string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_branch_ahead_behind: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Base) == "" {
		return "", errors.New("git_branch_ahead_behind: base is required")
	}
	if strings.TrimSpace(a.Head) == "" {
		a.Head = "HEAD"
	}
	// --left-right --count base...head prints "<only-in-base> <only-in-head>",
	// i.e. "behind ahead" from head's point of view.
	counts, err := gitOutput(ctx, t.Cwd.Get(), "rev-list", "--left-right", "--count", a.Base+"..."+a.Head)
	if err != nil {
		return "", fmt.Errorf("git_branch_ahead_behind: %w", err)
	}
	parts := strings.Fields(counts)
	if len(parts) != 2 {
		return "", fmt.Errorf("git_branch_ahead_behind: unexpected rev-list output %q", strings.TrimSpace(counts))
	}
	mergeBase, err := gitOutput(ctx, t.Cwd.Get(), "merge-base", a.Base, a.Head)
	if err != nil {
		return "", fmt.Errorf("git_branch_ahead_behind: %w", err)
	}
	return fmt.Sprintf("base=%s head=%s\nahead=%s behind=%s\nmerge_base=%s\n",
		a.Base, a.Head, parts[1], parts[0], strings.TrimSpace(mergeBase)), nil
}

// gitBranchDiffMaxCommits caps the commit list inside git_branch_diff so
// a long-lived branch doesn't turn the one-stop summary into a transcript.
const gitBranchDiffMaxCommits = 30

type GitBranchDiffTool struct{ Cwd *CwdRef }

func (t *GitBranchDiffTool) Name() string { return "git_branch_diff" }
func (t *GitBranchDiffTool) Description() string {
	return "One-stop branch review summary vs a base branch: merge base, ahead/behind counts, " +
		"commit list, changed files with status, and a diffstat — everything except the hunks. " +
		"Pull actual diffs afterwards with git_diff_files for just the files that matter."
}
func (t *GitBranchDiffTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base": map[string]any{"type": "string", "description": "Base branch to review against (e.g. main)"},
	}, "required": []string{"base"}}
}
func (t *GitBranchDiffTool) RequiresApproval(string) bool { return false }
func (t *GitBranchDiffTool) ParallelSafe(string) bool     { return true }
func (t *GitBranchDiffTool) PreviewCall(argsJSON string) string {
	var a struct{ Base string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_branch_diff(vs %s)", a.Base)
}
func (t *GitBranchDiffTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Base string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_branch_diff: invalid args: %w", err)
	}
	base := strings.TrimSpace(a.Base)
	if base == "" {
		return "", errors.New("git_branch_diff: base is required")
	}
	cwd := t.Cwd.Get()

	branch, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}
	mergeBase, err := gitOutput(ctx, cwd, "merge-base", base, "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}
	mb := strings.TrimSpace(mergeBase)

	counts, err := gitOutput(ctx, cwd, "rev-list", "--left-right", "--count", base+"...HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}
	ahead, behind := "?", "?"
	if parts := strings.Fields(counts); len(parts) == 2 {
		behind, ahead = parts[0], parts[1]
	}

	commits, err := gitOutput(ctx, cwd, "log", "--oneline",
		"-n", fmt.Sprintf("%d", gitBranchDiffMaxCommits), base+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}
	changed, err := gitOutput(ctx, cwd, "diff", "--name-status", mb, "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}
	stat, err := gitOutput(ctx, cwd, "diff", "--stat", mb, "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_diff: %w", err)
	}

	section := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "(none)\n"
		}
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nbranch=%s base=%s merge_base=%s\nahead=%s behind=%s\n",
		strings.TrimSpace(branch), base, mb, ahead, behind)
	fmt.Fprintf(&b, "## commits (newest first, capped at %d)\n%s", gitBranchDiffMaxCommits, section(commits))
	fmt.Fprintf(&b, "## changed-files\n%s", section(changed))
	fmt.Fprintf(&b, "## diffstat\n%s", section(stat))
	return b.String(), nil
}

type GitCommitAmendTool struct{ Cwd *CwdRef }

func (t *GitCommitAmendTool) Name() string { return "git_commit_amend" }
func (t *GitCommitAmendTool) Description() string {
	return "Amend the LAST commit: fold the currently staged changes into it, optionally replacing " +
		"its message (empty message keeps the existing one). REWRITES that commit — do not use " +
		"on commits that are already pushed unless the user explicitly wants a force-push."
}
func (t *GitCommitAmendTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"message": map[string]any{"type": "string", "description": "Replacement commit message (optional; empty keeps the current message)"},
	}}
}
func (t *GitCommitAmendTool) RequiresApproval(string) bool { return true }
func (t *GitCommitAmendTool) PreviewCall(argsJSON string) string {
	var a struct{ Message string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Message) == "" {
		return "⚠ rewrites the last commit (message kept)\n  $ git commit --amend --no-edit"
	}
	return fmt.Sprintf("⚠ rewrites the last commit\n  $ git commit --amend -m %q", a.Message)
}
func (t *GitCommitAmendTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Message string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_commit_amend: invalid args: %w", err)
	}
	args := []string{"commit", "--amend"}
	if strings.TrimSpace(a.Message) == "" {
		args = append(args, "--no-edit")
	} else {
		args = append(args, "-m", a.Message)
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), args...); err != nil {
		return "", fmt.Errorf("git_commit_amend: %w", err)
	}
	hash, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_commit_amend: %w", err)
	}
	return fmt.Sprintf("amended commit %s", strings.TrimSpace(hash)), nil
}

type GitCommitFixupTool struct{ Cwd *CwdRef }

func (t *GitCommitFixupTool) Name() string { return "git_commit_fixup" }
func (t *GitCommitFixupTool) Description() string {
	return "Create a fixup! commit from the staged changes targeting an earlier commit " +
		"(git commit --fixup=<commit>). The fixup squashes into its target on the next " +
		"`git rebase --autosquash` — the history rewrite happens at that later rebase, not here."
}
func (t *GitCommitFixupTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"commit": map[string]any{"type": "string", "description": "Target commit (sha or ref) the fixup amends"},
	}, "required": []string{"commit"}}
}
func (t *GitCommitFixupTool) RequiresApproval(string) bool { return true }
func (t *GitCommitFixupTool) PreviewCall(argsJSON string) string {
	var a struct{ Commit string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_commit_fixup(target=%s)", a.Commit)
}
func (t *GitCommitFixupTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Commit string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_commit_fixup: invalid args: %w", err)
	}
	target := strings.TrimSpace(a.Commit)
	if target == "" {
		return "", errors.New("git_commit_fixup: commit is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "commit", "--fixup="+target); err != nil {
		return "", fmt.Errorf("git_commit_fixup: %w", err)
	}
	hash, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_commit_fixup: %w", err)
	}
	return fmt.Sprintf("created fixup commit %s targeting %s (squashes on the next rebase --autosquash)",
		strings.TrimSpace(hash), target), nil
}
