package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/worktree"
)

// WorktreeStatusTool reports the clean/dirty state of a yottacode-
// managed worktree without entering it. Useful when the agent has
// multiple worktrees in flight and wants to decide which one to
// resume work in, or whether a sibling is safe to remove.
//
// Read-only and parallel-safe — never modifies anything.
type WorktreeStatusTool struct {
	Cwd *CwdRef
}

func (t *WorktreeStatusTool) Name() string { return "worktree_status" }

func (t *WorktreeStatusTool) Description() string {
	return "Report the clean/dirty state of a yottacode-managed worktree without entering it. " +
		"Args: name (optional — if omitted, returns a summary line for every yottacode " +
		"worktree in the repo). Reports branch, path, and any of: uncommitted changes, " +
		"untracked files, unpushed commits."
}

func (t *WorktreeStatusTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Worktree name. Omit to report on every yottacode worktree in the repo.",
			},
		},
	}
}

func (t *WorktreeStatusTool) RequiresApproval(string) bool { return false }
func (t *WorktreeStatusTool) ParallelSafe(string) bool     { return true }

func (t *WorktreeStatusTool) PreviewCall(argsJSON string) string {
	var a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Name == "" {
		return "worktree_status(all)"
	}
	return fmt.Sprintf("worktree_status(%s)", a.Name)
}

func (t *WorktreeStatusTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("worktree_status: invalid args: %w", err)
	}

	repoRoot, err := worktree.ResolveRepoRoot(ctx, t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("worktree_status: %w", err)
	}

	if a.Name != "" {
		if err := worktree.ValidateName(a.Name); err != nil {
			return "", err
		}
		return statusLine(ctx, repoRoot, a.Name, worktree.Dir(repoRoot, a.Name))
	}

	// No name: summarize every yottacode worktree.
	infos, err := worktree.List(ctx, repoRoot)
	if err != nil {
		return "", fmt.Errorf("worktree_status: list: %w", err)
	}
	var b strings.Builder
	for _, w := range infos {
		name, ok := worktree.IsWorktreePath(repoRoot, w.Path)
		if !ok {
			continue // skip the main worktree
		}
		line, err := statusLine(ctx, repoRoot, name, w.Path)
		if err != nil {
			b.WriteString(fmt.Sprintf("%s: error: %v\n", name, err))
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "no yottacode worktrees in this repo", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func statusLine(ctx context.Context, repoRoot, name, wtDir string) (string, error) {
	state, err := worktree.DetectState(ctx, wtDir)
	if err != nil {
		return "", err
	}
	tag := "clean"
	if !state.Clean() {
		tag = "dirty: " + strings.Join(state.Reasons(), ", ")
	}
	return fmt.Sprintf("%s  branch=%s  path=%s  %s", name, worktree.Branch(name), wtDir, tag), nil
}
