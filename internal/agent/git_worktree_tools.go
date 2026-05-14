package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GitWorktreeListTool lists all worktrees for the current repository.
type GitWorktreeListTool struct{ Cwd string }

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
	out, err := gitOutput(ctx, t.Cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git_worktree_list: %w", err)
	}
	return out, nil
}

// GitWorktreeAddTool adds a new worktree.
type GitWorktreeAddTool struct{ Cwd string }

func (t *GitWorktreeAddTool) Name() string { return "git_worktree_add" }
func (t *GitWorktreeAddTool) Description() string {
	return "Add a new worktree at <path> with optional branch <branch>."
}
func (t *GitWorktreeAddTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":  map[string]any{"type": "string", "description": "Path for the new worktree"},
		"branch": map[string]any{"type": "string", "description": "Branch name to create/checkout (optional)"},
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
	args := []string{"worktree", "add", a.Path}
	if a.Branch != "" {
		args = append(args, "-b", a.Branch)
	}
	if _, err := gitOutput(ctx, t.Cwd, args...); err != nil {
		return "", fmt.Errorf("git_worktree_add: %w", err)
	}
	return fmt.Sprintf("added worktree at %s", a.Path), nil
}

// GitWorktreeRemoveTool removes a worktree.
type GitWorktreeRemoveTool struct{ Cwd string }

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
	if _, err := gitOutput(ctx, t.Cwd, "worktree", "remove", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_remove: %w", err)
	}
	return fmt.Sprintf("removed worktree at %s", a.Path), nil
}

// GitWorktreeLockTool locks a worktree.
type GitWorktreeLockTool struct{ Cwd string }

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
	if _, err := gitOutput(ctx, t.Cwd, "worktree", "lock", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_lock: %w", err)
	}
	return fmt.Sprintf("locked worktree at %s", a.Path), nil
}

// GitWorktreeUnlockTool unlocks a worktree.
type GitWorktreeUnlockTool struct{ Cwd string }

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
	if _, err := gitOutput(ctx, t.Cwd, "worktree", "unlock", a.Path); err != nil {
		return "", fmt.Errorf("git_worktree_unlock: %w", err)
	}
	return fmt.Sprintf("unlocked worktree at %s", a.Path), nil
}

// GitWorktreePruneTool prunes stale worktree data.
type GitWorktreePruneTool struct{ Cwd string }

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
	if _, err := gitOutput(ctx, t.Cwd, "worktree", "prune"); err != nil {
		return "", fmt.Errorf("git_worktree_prune: %w", err)
	}
	return "pruned stale worktree data", nil
}