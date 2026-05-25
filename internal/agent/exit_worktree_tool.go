package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yottadynamics/yottacode/internal/worktree"
)

// ExitWorktreeTool leaves the named yottacode worktree, applying the
// cleanup rule. The session itself continues — this is symmetric to
// EnterWorktreeTool, which only created the worktree without
// swapping cwd. Cleanup behavior:
//
//	cleanup=auto (default): clean tree → remove; dirty tree → keep
//	                        with a "use cleanup=remove to discard"
//	                        hint in the result
//	cleanup=keep:           never remove
//	cleanup=remove:         force-remove (discards uncommitted /
//	                        untracked / unpushed work)
//
// Always requires approval (auto-mode safety floor). The
// force-remove path is destructive and the user should see it.
type ExitWorktreeTool struct {
	Cwd *CwdRef
}

func (t *ExitWorktreeTool) Name() string { return "exit_worktree" }

func (t *ExitWorktreeTool) Description() string {
	return "Leave a yottacode-managed worktree, optionally removing it. " +
		"Args: name (omit to use the worktree the session is currently in), " +
		"cleanup ('auto' default — remove if clean, keep if dirty; 'keep' " +
		"never removes; 'remove' force-removes and discards uncommitted / " +
		"untracked / unpushed work). Returns what was done."
}

func (t *ExitWorktreeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Worktree name. If omitted, inferred from the session's current working directory (errors if cwd isn't a yottacode worktree).",
			},
			"cleanup": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "keep", "remove"},
				"description": "'auto' (default): remove if clean, keep if dirty. 'keep': never remove. 'remove': force-remove (destructive).",
			},
		},
	}
}

func (t *ExitWorktreeTool) RequiresApproval(string) bool { return true }

func (t *ExitWorktreeTool) PreviewCall(argsJSON string) string {
	var a struct {
		Name    string `json:"name"`
		Cleanup string `json:"cleanup"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	name := a.Name
	if name == "" {
		name = "<inferred-from-cwd>"
	}
	cleanup := a.Cleanup
	if cleanup == "" {
		cleanup = "auto"
	}
	prefix := ""
	if cleanup == "remove" {
		prefix = "⚠ DESTRUCTIVE: "
	}
	return fmt.Sprintf("%sexit_worktree(name=%s, cleanup=%s)", prefix, name, cleanup)
}

func (t *ExitWorktreeTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Name    string `json:"name"`
		Cleanup string `json:"cleanup"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("exit_worktree: invalid args: %w", err)
	}
	cleanup := a.Cleanup
	if cleanup == "" {
		cleanup = "auto"
	}
	if cleanup != "auto" && cleanup != "keep" && cleanup != "remove" {
		return "", fmt.Errorf("exit_worktree: cleanup must be 'auto', 'keep', or 'remove', got %q", cleanup)
	}

	repoRoot, err := worktree.ResolveRepoRoot(ctx, t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("exit_worktree: %w", err)
	}

	name := a.Name
	if name == "" {
		inferred, ok := worktree.IsWorktreePath(repoRoot, t.Cwd.Get())
		if !ok {
			return "", fmt.Errorf("exit_worktree: name omitted and session cwd %q is not under <repo>/.yottacode/worktrees/; pass name explicitly", t.Cwd.Get())
		}
		name = inferred
	} else if err := worktree.ValidateName(name); err != nil {
		return "", err
	}

	wtDir := worktree.Dir(repoRoot, name)

	// If the session's cwd is *inside* the worktree we're about to
	// touch, swap back to the repo root BEFORE the removal — git
	// worktree remove refuses to nuke a directory it's standing in.
	// Even for the keep / dirty paths, we swap so the session leaves
	// the worktree cleanly.
	sessionInThisWorktree := false
	if inferredName, ok := worktree.IsWorktreePath(repoRoot, t.Cwd.Get()); ok && inferredName == name {
		sessionInThisWorktree = true
		if err := t.swapCwd(repoRoot); err != nil {
			return "", err
		}
	}

	switch cleanup {
	case "keep":
		if sessionInThisWorktree {
			return fmt.Sprintf("kept worktree %s at %s (cleanup=keep); session cwd is now %s", name, wtDir, repoRoot), nil
		}
		return fmt.Sprintf("kept worktree %s at %s (cleanup=keep)", name, wtDir), nil

	case "remove":
		// Force-remove regardless of state — the user explicitly
		// opted into discarding everything.
		if err := worktree.Remove(ctx, repoRoot, wtDir, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed worktree %s (force; any uncommitted work discarded)", name), nil

	default: // "auto"
		state, err := worktree.DetectState(ctx, wtDir)
		if err != nil {
			return "", err
		}
		if state.Clean() {
			if err := worktree.Remove(ctx, repoRoot, wtDir, false); err != nil {
				return "", err
			}
			if sessionInThisWorktree {
				return fmt.Sprintf("removed clean worktree %s (branch %s also deleted); session cwd is now %s", name, worktree.Branch(name), repoRoot), nil
			}
			return fmt.Sprintf("removed clean worktree %s at %s (branch %s also deleted)", name, wtDir, worktree.Branch(name)), nil
		}
		// Dirty — keep, surface reasons.
		suffix := ""
		if sessionInThisWorktree {
			suffix = fmt.Sprintf(" Session cwd is now %s.", repoRoot)
		}
		return fmt.Sprintf(
			"kept worktree %s at %s — dirty: %s. To discard, call exit_worktree again with cleanup=\"remove\".%s",
			name, wtDir, strings.Join(state.Reasons(), ", "), suffix,
		), nil
	}
}

// swapCwd updates the shared CwdRef and the process's working dir
// back to the parent repo root before the worktree is touched. Both
// updates are needed: CwdRef.Get() drives tool-level path resolution,
// and the process cwd drives exec'd children.
func (t *ExitWorktreeTool) swapCwd(newDir string) error {
	if t.Cwd != nil {
		t.Cwd.Set(newDir)
	}
	if err := os.Chdir(newDir); err != nil {
		return fmt.Errorf("exit_worktree: chdir %s: %w", newDir, err)
	}
	return nil
}

