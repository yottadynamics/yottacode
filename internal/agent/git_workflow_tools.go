package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

type GitBranchStatusTool struct{ Cwd *CwdRef }

func (t *GitBranchStatusTool) Name() string { return "git_branch_status" }
func (t *GitBranchStatusTool) Description() string {
	return "Show the current branch, upstream, ahead/behind counts, and whether the working tree is dirty."
}
func (t *GitBranchStatusTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *GitBranchStatusTool) RequiresApproval(string) bool { return false }
func (t *GitBranchStatusTool) ParallelSafe(string) bool     { return true }
func (t *GitBranchStatusTool) PreviewCall(string) string    { return "git_branch_status()" }
func (t *GitBranchStatusTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git_branch_status: git binary not found in PATH")
	}
	branch, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_branch_status: %w", err)
	}
	upstream, _ := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	// Ask git for tracked/staged changes separately from untracked files so
	// generated repo-local caches can be summarized instead of flooding context.
	statusShort, err := gitOutput(ctx, t.Cwd.Get(), "status", "--short", "--untracked-files=no")
	if err != nil {
		return "", fmt.Errorf("git_branch_status: %w", err)
	}
	untracked, _ := boundedUntrackedFiles(ctx, t.Cwd.Get())
	statusForRender := strings.TrimRight(statusShort, "\n")
	statusForRender = strings.TrimRight(statusForRender+"\n"+strings.TrimSpace(untracked), "\n")
	dirty := strings.TrimSpace(statusForRender) != ""
	aheadBehind := ""
	if strings.TrimSpace(upstream) != "" {
		counts, err := gitOutput(ctx, t.Cwd.Get(), "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
		if err == nil {
			parts := strings.Fields(counts)
			if len(parts) == 2 {
				aheadBehind = fmt.Sprintf("ahead=%s behind=%s", parts[0], parts[1])
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "branch=%s\n", strings.TrimSpace(branch))
	if strings.TrimSpace(upstream) != "" {
		fmt.Fprintf(&b, "upstream=%s\n", strings.TrimSpace(upstream))
	}
	if aheadBehind != "" {
		fmt.Fprintf(&b, "%s\n", aheadBehind)
	}
	fmt.Fprintf(&b, "dirty=%v\n", dirty)
	if dirty {
		b.WriteString("--- status --short ---\n")
		b.WriteString(statusForRender)
		if !strings.HasSuffix(statusForRender, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

type GitShowFileAtRevTool struct{ Cwd *CwdRef }

func (t *GitShowFileAtRevTool) Name() string { return "git_show_file_at_rev" }
func (t *GitShowFileAtRevTool) Description() string {
	return "Show the contents of a file at a specific git revision without changing the working tree."
}
func (t *GitShowFileAtRevTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Path to the file within the repo"},
		"rev":  map[string]any{"type": "string", "description": "Revision or ref (default HEAD)"},
	}, "required": []string{"path"}}
}
func (t *GitShowFileAtRevTool) RequiresApproval(string) bool { return false }
func (t *GitShowFileAtRevTool) ParallelSafe(string) bool     { return true }
func (t *GitShowFileAtRevTool) PreviewCall(argsJSON string) string {
	var a struct{ Path, Rev string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Rev) == "" {
		a.Rev = "HEAD"
	}
	return fmt.Sprintf("git_show_file_at_rev(%s @ %s)", a.Path, a.Rev)
}
func (t *GitShowFileAtRevTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Path, Rev string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_show_file_at_rev: invalid args: %w", err)
	}
	path := strings.TrimSpace(a.Path)
	rev := strings.TrimSpace(a.Rev)
	if path == "" {
		return "", errors.New("git_show_file_at_rev: path is required")
	}
	if rev == "" {
		rev = "HEAD"
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), "show", rev+":"+filepath.ToSlash(path))
	if err != nil {
		return "", fmt.Errorf("git_show_file_at_rev: %w", err)
	}
	return out, nil
}

type GitDiffFilesTool struct{ Cwd *CwdRef }

func (t *GitDiffFilesTool) Name() string { return "git_diff_files" }
func (t *GitDiffFilesTool) Description() string {
	return "Show a git diff for specific files or refs. Useful for review and change inspection."
}
func (t *GitDiffFilesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base":  map[string]any{"type": "string", "description": "Base revision (optional)"},
		"head":  map[string]any{"type": "string", "description": "Head revision (optional)"},
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional file paths"},
	}}
}
func (t *GitDiffFilesTool) RequiresApproval(string) bool { return false }
func (t *GitDiffFilesTool) ParallelSafe(string) bool     { return true }
func (t *GitDiffFilesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Base, Head string
		Paths      []string
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_diff_files(base=%s, head=%s, paths=%d)", a.Base, a.Head, len(a.Paths))
}
func (t *GitDiffFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Base, Head string
		Paths      []string
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_diff_files: invalid args: %w", err)
	}
	base := strings.TrimSpace(a.Base)
	head := strings.TrimSpace(a.Head)
	paths := trimStrings(a.Paths)
	args := []string{"diff"}
	switch {
	case base != "" && head != "":
		args = append(args, base, head)
	case base != "":
		args = append(args, base)
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), args...)
	if err != nil {
		return "", fmt.Errorf("git_diff_files: %w", err)
	}
	if out == "" {
		return "(no diff)\n", nil
	}
	return out, nil
}

type GitStageFilesTool struct{ Cwd *CwdRef }

func (t *GitStageFilesTool) Name() string { return "git_stage_files" }
func (t *GitStageFilesTool) Description() string {
	return "Stage files in git. Either pass `paths` for a specific set (git add -- ...) or `all: true` to stage every tracked, untracked, and deleted change (git add -A). The two modes are mutually exclusive."
}
func (t *GitStageFilesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to stage. Mutually exclusive with all."},
		"all":   map[string]any{"type": "boolean", "description": "Stage all tracked, untracked, and deleted files (git add -A). Mutually exclusive with paths."},
	}}
}
func (t *GitStageFilesTool) RequiresApproval(string) bool { return true }
func (t *GitStageFilesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Paths []string `json:"paths"`
		All   bool     `json:"all"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.All {
		return "git_stage_files(all)"
	}
	return fmt.Sprintf("git_stage_files(%s)", strings.Join(a.Paths, ", "))
}
func (t *GitStageFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Paths []string `json:"paths"`
		All   bool     `json:"all"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_stage_files: invalid args: %w", err)
	}
	if a.All && len(a.Paths) > 0 {
		return "", errors.New("git_stage_files: paths and all are mutually exclusive")
	}
	if !a.All && len(a.Paths) == 0 {
		return "", errors.New("git_stage_files: paths or all is required")
	}
	if a.All {
		if _, err := gitOutput(ctx, t.Cwd.Get(), "add", "-A"); err != nil {
			return "", fmt.Errorf("git_stage_files: %w", err)
		}
		return "staged all changes", nil
	}
	args := append([]string{"add", "--"}, a.Paths...)
	if _, err := gitOutput(ctx, t.Cwd.Get(), args...); err != nil {
		return "", fmt.Errorf("git_stage_files: %w", err)
	}
	return fmt.Sprintf("staged %d file(s)", len(a.Paths)), nil
}

type GitUnstageFilesTool struct{ Cwd *CwdRef }

func (t *GitUnstageFilesTool) Name() string { return "git_unstage_files" }
func (t *GitUnstageFilesTool) Description() string {
	return "Unstage specific files in git (git reset HEAD -- ...)."
}
func (t *GitUnstageFilesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to unstage"},
	}, "required": []string{"paths"}}
}
func (t *GitUnstageFilesTool) RequiresApproval(string) bool { return true }
func (t *GitUnstageFilesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_unstage_files(%s)", strings.Join(a.Paths, ", "))
}
func (t *GitUnstageFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_unstage_files: invalid args: %w", err)
	}
	if len(a.Paths) == 0 {
		return "", errors.New("git_unstage_files: paths is required")
	}
	args := append([]string{"reset", "HEAD", "--"}, a.Paths...)
	if _, err := gitOutput(ctx, t.Cwd.Get(), args...); err != nil {
		return "", fmt.Errorf("git_unstage_files: %w", err)
	}
	return fmt.Sprintf("unstaged %d file(s)", len(a.Paths)), nil
}

type GitCreateBranchTool struct {
	Cwd        *CwdRef
	LSPManager *lspci.Manager
}

func (t *GitCreateBranchTool) Name() string { return "git_create_branch" }
func (t *GitCreateBranchTool) Description() string {
	return "Create a new git branch and switch to it (git switch -c <name> [<start_point>]). Refuses if the branch already exists locally."
}
func (t *GitCreateBranchTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"name":        map[string]any{"type": "string", "description": "Branch name to create"},
		"start_point": map[string]any{"type": "string", "description": "Optional starting ref (commit/branch/tag). Defaults to HEAD."},
	}, "required": []string{"name"}}
}
func (t *GitCreateBranchTool) RequiresApproval(string) bool { return true }
func (t *GitCreateBranchTool) PreviewCall(argsJSON string) string {
	var a struct {
		Name       string `json:"name"`
		StartPoint string `json:"start_point"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.StartPoint) != "" {
		return fmt.Sprintf("git_create_branch(name=%s from=%s)", a.Name, a.StartPoint)
	}
	return fmt.Sprintf("git_create_branch(name=%s)", a.Name)
}
func (t *GitCreateBranchTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Name       string `json:"name"`
		StartPoint string `json:"start_point"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_create_branch: invalid args: %w", err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", errors.New("git_create_branch: name is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "check-ref-format", "--branch", name); err != nil {
		return "", fmt.Errorf("git_create_branch: invalid branch name %q: %w", name, err)
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "--verify", "refs/heads/"+name); err == nil {
		return "", fmt.Errorf("git_create_branch: branch_exists: %q already exists locally", name)
	}
	args := []string{"switch", "-c", name}
	if sp := strings.TrimSpace(a.StartPoint); sp != "" {
		args = append(args, sp)
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), args...); err != nil {
		return "", fmt.Errorf("git_create_branch: %w", err)
	}
	fromSHA, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_create_branch: %w", err)
	}
	out := fmt.Sprintf("created=true branch=%s from=%s", name, strings.TrimSpace(fromSHA))
	if note := invalidateLSPServers(t.LSPManager); note != "" {
		out += "\n" + note
	}
	return out, nil
}

type GitCommitTool struct{ Cwd *CwdRef }

func (t *GitCommitTool) Name() string { return "git_commit" }
func (t *GitCommitTool) Description() string {
	return "Create a git commit from the currently staged changes."
}
func (t *GitCommitTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"message": map[string]any{"type": "string", "description": "Commit message"},
	}, "required": []string{"message"}}
}
func (t *GitCommitTool) RequiresApproval(string) bool { return true }
func (t *GitCommitTool) PreviewCall(argsJSON string) string {
	var a struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_commit(%q)", a.Message)
}
func (t *GitCommitTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_commit: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Message) == "" {
		return "", errors.New("git_commit: message is required")
	}
	if _, err := gitOutput(ctx, t.Cwd.Get(), "commit", "-m", a.Message); err != nil {
		return "", fmt.Errorf("git_commit: %w", err)
	}
	hash, err := gitOutput(ctx, t.Cwd.Get(), "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git_commit: %w", err)
	}
	return fmt.Sprintf("created commit %s", strings.TrimSpace(hash)), nil
}

type GitLogFileTool struct{ Cwd *CwdRef }

func (t *GitLogFileTool) Name() string        { return "git_log_file" }
func (t *GitLogFileTool) Description() string { return "Show git history for a single file." }
func (t *GitLogFileTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":  map[string]any{"type": "string", "description": "Path to the file"},
		"limit": map[string]any{"type": "integer", "description": "Max commits to show (default 10)"},
	}, "required": []string{"path"}}
}
func (t *GitLogFileTool) RequiresApproval(string) bool { return false }
func (t *GitLogFileTool) ParallelSafe(string) bool     { return true }
func (t *GitLogFileTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_log_file(%s)", a.Path)
}
func (t *GitLogFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_log_file: invalid args: %w", err)
	}
	path := strings.TrimSpace(a.Path)
	if path == "" {
		return "", errors.New("git_log_file: path is required")
	}
	if a.Limit <= 0 {
		a.Limit = 10
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), "log", "--oneline", "-n", fmt.Sprintf("%d", a.Limit), "--", path)
	if err != nil {
		return "", fmt.Errorf("git_log_file: %w", err)
	}
	if out == "" {
		return "(no history)\n", nil
	}
	return out, nil
}

type GitBlameLinesTool struct{ Cwd *CwdRef }

func (t *GitBlameLinesTool) Name() string        { return "git_blame_lines" }
func (t *GitBlameLinesTool) Description() string { return "Show git blame for a line range in a file." }
func (t *GitBlameLinesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":  map[string]any{"type": "string", "description": "Path to the file"},
		"start": map[string]any{"type": "integer", "description": "Start line (1-based)"},
		"end":   map[string]any{"type": "integer", "description": "End line (inclusive)"},
	}, "required": []string{"path", "start", "end"}}
}
func (t *GitBlameLinesTool) RequiresApproval(string) bool { return false }
func (t *GitBlameLinesTool) ParallelSafe(string) bool     { return true }
func (t *GitBlameLinesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path       string `json:"path"`
		Start, End int
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_blame_lines(%s:%d-%d)", a.Path, a.Start, a.End)
}
func (t *GitBlameLinesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		Start, End int
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_blame_lines: invalid args: %w", err)
	}
	path := strings.TrimSpace(a.Path)
	if path == "" || a.Start <= 0 || a.End < a.Start {
		return "", errors.New("git_blame_lines: path, start, and end are required and must form a valid range")
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), "blame", "-L", fmt.Sprintf("%d,%d", a.Start, a.End), "--", path)
	if err != nil {
		return "", fmt.Errorf("git_blame_lines: %w", err)
	}
	return out, nil
}

type GitMergeBaseTool struct{ Cwd *CwdRef }

func (t *GitMergeBaseTool) Name() string        { return "git_merge_base" }
func (t *GitMergeBaseTool) Description() string { return "Find the merge base between two refs." }
func (t *GitMergeBaseTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"base": map[string]any{"type": "string", "description": "First ref"},
		"head": map[string]any{"type": "string", "description": "Second ref"},
	}, "required": []string{"base", "head"}}
}
func (t *GitMergeBaseTool) RequiresApproval(string) bool { return false }
func (t *GitMergeBaseTool) ParallelSafe(string) bool     { return true }
func (t *GitMergeBaseTool) PreviewCall(argsJSON string) string {
	var a struct{ Base, Head string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("git_merge_base(%s, %s)", a.Base, a.Head)
}
func (t *GitMergeBaseTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct{ Base, Head string }
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_merge_base: invalid args: %w", err)
	}
	base := strings.TrimSpace(a.Base)
	head := strings.TrimSpace(a.Head)
	if base == "" || head == "" {
		return "", errors.New("git_merge_base: base and head are required")
	}
	out, err := gitOutput(ctx, t.Cwd.Get(), "merge-base", base, head)
	if err != nil {
		return "", fmt.Errorf("git_merge_base: %w", err)
	}
	return out, nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git binary not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func trimStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
