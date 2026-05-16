package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GitWorktreeListTool lists all worktrees for the current repository.
type GitWorktreeListTool struct{ Cwd *CwdRef }

func (t *GitWorktreeListTool) Name() string { return "git_worktree_list" }
func (t *GitWorktreeListTool) Description() string {
	return "List all worktrees of the current repository."
}
func (t *GitWorktreeListTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *GitWorktreeListTool) RequiresApproval(string) bool { return false }
func (t *GitWorktreeListTool) ParallelSafe(string) bool     { return true }
func (t *GitWorktreeListTool) PreviewCall(string) string    { return "git_worktree_list()" }
func (t *GitWorktreeListTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	out, err := gitOutput(ctx, t.Cwd.Get(), "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git_worktree_list: %w", err)
	}
	return out, nil
}

// GitWorktreeAddTool adds a new worktree.
type GitWorktreeAddTool struct{ Cwd *CwdRef }

func (t *GitWorktreeAddTool) Name() string { return "git_worktree_add" }
func (t *GitWorktreeAddTool) Description() string {
	return "LOW-LEVEL `git worktree add <path>` wrapper. For the common 'create " +
		"a worktree called X' case, prefer enter_worktree — it auto-resolves " +
		"the path under <repo>/.yottacode/worktrees/<name>/, generates a name " +
		"if missing, copies .worktreeinclude, and swaps the session cwd. Use " +
		"this tool only when you need to place a worktree at an explicit custom " +
		"path outside that convention. Path is REQUIRED and must be an absolute " +
		"or repo-relative path (not a bare name); branch is optional — if it " +
		"exists locally it's checked out, otherwise a new branch is created."
}
func (t *GitWorktreeAddTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{
			"type": "string",
			"description": "Explicit absolute or repo-relative path for the new worktree directory " +
				"(e.g. '/abs/path/to/sibling-worktree' or '../sibling-worktree'). NOT a bare name — " +
				"for that use enter_worktree, which resolves under .yottacode/worktrees/<name>/ automatically.",
		},
		"branch": map[string]any{"type": "string", "description": "Branch name to checkout (if exists) or create and checkout (optional)"},
	}, "required": []string{"path"}}
}
func (t *GitWorktreeAddTool) RequiresApproval(string) bool { return true }
func (t *GitWorktreeAddTool) ParallelSafe(string) bool     { return false }
func (t *GitWorktreeAddTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Branch == "" {
		return fmt.Sprintf("git_worktree_add(%s)", a.Path)
	}
	// Note: PreviewCall cannot know if branch exists, so we show the create form
	// The actual behavior will depend on branch existence
	return fmt.Sprintf("git_worktree_add(%s, -b %s)", a.Path, a.Branch)
}
func (t *GitWorktreeAddTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_worktree_add: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("git_worktree_add: path is required")
	}

	// Capture branch state BEFORE the git call. After `git worktree add -b`
	// the branch always exists, so a post-call branchExists() check would
	// always return true and the wrong message would be returned.
	var args []string
	branchExistedBefore := false
	switch {
	case a.Branch == "":
		args = []string{"worktree", "add", a.Path}
	case branchExists(ctx, t.Cwd.Get(), a.Branch):
		branchExistedBefore = true
		args = []string{"worktree", "add", a.Path, a.Branch}
	default:
		args = []string{"worktree", "add", "-b", a.Branch, a.Path}
	}

	if _, err := gitOutput(ctx, t.Cwd.Get(), args...); err != nil {
		return "", fmt.Errorf("git_worktree_add: %w", err)
	}

	switch {
	case a.Branch == "":
		return fmt.Sprintf("added worktree at %s (from HEAD)", a.Path), nil
	case branchExistedBefore:
		return fmt.Sprintf("added worktree at %s (from existing branch %s)", a.Path, a.Branch), nil
	default:
		return fmt.Sprintf("added worktree at %s (created and checked out new branch %s)", a.Path, a.Branch), nil
	}
}

// branchExists checks if a branch exists locally in the repository
func branchExists(ctx context.Context, cwd, branch string) bool {
	output, err := gitOutput(ctx, cwd, "rev-parse", "--verify", branch)
	return err == nil && strings.TrimSpace(output) != ""
}

// GitWorktreeRemoveTool removes a worktree.
type GitWorktreeRemoveTool struct{ Cwd *CwdRef }

func (t *GitWorktreeRemoveTool) Name() string { return "git_worktree_remove" }
func (t *GitWorktreeRemoveTool) Description() string {
	return "Remove a worktree at <path>."
}
func (t *GitWorktreeRemoveTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Path of the worktree to remove"},
	}, "required": []string{"path"}}
}
func (t *GitWorktreeRemoveTool) RequiresApproval(string) bool { return true }
func (t *GitWorktreeRemoveTool) ParallelSafe(string) bool     { return false }
func (t *GitWorktreeRemoveTool) PreviewCall(argsJSON string) string {
	var a struct{ Path string `json:"path"` }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_worktree_remove(%s)", a.Path)
}
func (t *GitWorktreeRemoveTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Path string `json:"path"` }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_worktree_remove: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("git_worktree_remove: path is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "worktree", "remove", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_remove: %w", err)
	}
	return fmt.Sprintf("removed worktree at %s", a.Path), nil
}

// GitWorktreeLockTool locks a worktree.
type GitWorktreeLockTool struct{ Cwd *CwdRef }

func (t *GitWorktreeLockTool) Name() string { return "git_worktree_lock" }
func (t *GitWorktreeLockTool) Description() string {
	return "Lock a worktree at <path> to prevent it from being pruned or removed."
}
func (t *GitWorktreeLockTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Path of the worktree to lock"},
	}, "required": []string{"path"}}
}
func (t *GitWorktreeLockTool) RequiresApproval(string) bool { return true }
func (t *GitWorktreeLockTool) ParallelSafe(string) bool     { return false }
func (t *GitWorktreeLockTool) PreviewCall(argsJSON string) string {
	var a struct{ Path string `json:"path"` }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_worktree_lock(%s)", a.Path)
}
func (t *GitWorktreeLockTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Path string `json:"path"` }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_worktree_lock: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("git_worktree_lock: path is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "worktree", "lock", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_lock: %w", err)
	}
	return fmt.Sprintf("locked worktree at %s", a.Path), nil
}

// GitWorktreeUnlockTool unlocks a worktree.
type GitWorktreeUnlockTool struct{ Cwd *CwdRef }

func (t *GitWorktreeUnlockTool) Name() string { return "git_worktree_unlock" }
func (t *GitWorktreeUnlockTool) Description() string {
	return "Unlock a worktree at <path> to allow it to be pruned or removed."
}
func (t *GitWorktreeUnlockTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Path of the worktree to unlock"},
	}, "required": []string{"path"}}
}
func (t *GitWorktreeUnlockTool) RequiresApproval(string) bool { return true }
func (t *GitWorktreeUnlockTool) ParallelSafe(string) bool     { return false }
func (t *GitWorktreeUnlockTool) PreviewCall(argsJSON string) string {
	var a struct{ Path string `json:"path"` }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_worktree_unlock(%s)", a.Path)
}
func (t *GitWorktreeUnlockTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Path string `json:"path"` }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_worktree_unlock: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("git_worktree_unlock: path is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "worktree", "unlock", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_unlock: %w", err)
	}
	return fmt.Sprintf("unlocked worktree at %s", a.Path), nil
}

// GitWorktreePruneTool prunes stale worktree data.
type GitWorktreePruneTool struct{ Cwd *CwdRef }

func (t *GitWorktreePruneTool) Name() string { return "git_worktree_prune" }
func (t *GitWorktreePruneTool) Description() string {
	return "Prune stale worktree data from the repository."
}
func (t *GitWorktreePruneTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *GitWorktreePruneTool) RequiresApproval(string) bool { return true }
func (t *GitWorktreePruneTool) ParallelSafe(string) bool     { return false }
func (t *GitWorktreePruneTool) PreviewCall(string) string    { return "git_worktree_prune()" }
func (t *GitWorktreePruneTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if _, err := gitOutput(ctx, t.Cwd.Get(), "worktree", "prune"); err != nil {
		return "", fmt.Errorf("git_worktree_prune: %w", err)
	}
	return "pruned stale worktree data", nil
}

