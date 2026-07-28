package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yottadynamics/yottacode/internal/permissions"
)

type ApplyDiffTool struct {
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *ApplyDiffTool) Name() string { return "apply_diff" }
func (t *ApplyDiffTool) Description() string {
	return "Apply a unified diff patch to files in the working tree using git apply. Rejects apply_patch-style patches with a targeted error and validates target paths before patching."
}
func (t *ApplyDiffTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"diff": map[string]any{"type": "string", "description": "Unified diff to apply"},
		},
		"required": []string{"diff"},
	}
}
func (t *ApplyDiffTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports every file the diff touches so /checkpoints
// can restore each pre-image. Uses the same ParseDiffPaths the tool
// already runs during validation, so the snapshot set matches the
// validation set by construction.
func (t *ApplyDiffTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || strings.TrimSpace(a.Diff) == "" {
		return nil
	}
	// Repair first so the snapshot set matches the paths Execute will
	// actually validate and patch (see Execute's repair call). Otherwise a
	// fully-escaped diff snapshots a garbage path and /checkpoints captures
	// no pre-image for the real target.
	rels := permissions.ParseDiffPaths(repairFullyEscapedDiff(a.Diff))
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, rel)
		}
		out = append(out, abs)
	}
	return out
}

func (t *ApplyDiffTool) PreviewCall(argsJSON string) string {
	var a struct {
		Diff string `json:"diff"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	preview := a.Diff
	if len(preview) > 400 {
		preview = preview[:400] + "…"
	}
	return "apply_diff\n" + preview
}
func (t *ApplyDiffTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("apply_diff: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Diff) == "" {
		return "", fmt.Errorf("apply_diff: diff is required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("apply_diff: git binary not found in PATH")
	}
	// Repair a fully JSON-escaped diff BEFORE anything reads it. This must
	// precede path parsing: ParseDiffPaths and ValidateWritePath have to
	// see the exact bytes `git apply` will receive. If the repair ran after
	// validation (as it once did), an escaped diff would be validated as a
	// single garbage "path" that matches no deny entry and resolves under
	// cwd, then repaired into a real multi-line patch targeting a denied
	// file — a deny-list bypass (self-granting a permissions rule) and,
	// symmetrically, a spurious "no recognizable file headers" rejection of
	// a diff the repair would have made applyable.
	a.Diff = normalizeModelPatch(repairFullyEscapedDiff(a.Diff))
	// Parse target paths out of the diff header and run them through
	// the same write-path validator write_file/edit_file use, so
	// DefaultDenyPaths (yottacode state, .git internals, etc.) and the
	// cwd / --allow-paths confinement apply here too. Without this,
	// `git apply` would happily patch ../../etc/whatever as long as the
	// diff said so, bypassing the rest of the safety story.
	paths := permissions.ParseDiffPaths(a.Diff)
	if len(paths) == 0 {
		if strings.Contains(a.Diff, "*** Begin Patch") || strings.Contains(a.Diff, "*** Update File:") {
			return "", fmt.Errorf("apply_diff: apply_patch-style patches are not accepted here — use a unified diff with `--- a/PATH` / `+++ b/PATH` headers")
		}
		// Echo a bounded, quoted snippet of what the parser actually saw
		// (post-repair — the same bytes ParseDiffPaths read). The failure
		// is usually an invisible one: a single JSON-escaped line, `@@`
		// hunks with no `---`/`+++`, or whitespace that ate the header. %q
		// surfaces newlines/tabs so the shape is legible, and naming the
		// expected header makes the error self-correcting. Mirrors the
		// git-apply failure path below, which already echoes patch=%q.
		got := a.Diff
		if len(got) > 200 {
			got = got[:200] + "…"
		}
		return "", fmt.Errorf("apply_diff: diff contains no recognizable file headers — "+
			"expected a `--- a/PATH` / `+++ b/PATH` header pair (or `rename from`/`rename to`); got %q", got)
	}
	for _, rel := range paths {
		if err := validateUnifiedDiffHunks(a.Diff, rel); err != nil {
			return "", fmt.Errorf("apply_diff: %w", err)
		}
	}
	for _, rel := range paths {
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(t.Cwd.Get(), rel)
		}
		if err := ValidateWritePath(abs, t.WriteOpts); err != nil {
			return "", fmt.Errorf("apply_diff: %w", err)
		}
	}
	patch, err := os.CreateTemp(t.Cwd.Get(), "yottacode-apply-*.diff")
	if err != nil {
		return "", fmt.Errorf("apply_diff: create temp patch: %w", err)
	}
	patchPath := patch.Name()
	defer os.Remove(patchPath)
	if _, err := patch.WriteString(a.Diff); err != nil {
		patch.Close()
		return "", fmt.Errorf("apply_diff: write temp patch: %w", err)
	}
	if err := patch.Close(); err != nil {
		return "", fmt.Errorf("apply_diff: close temp patch: %w", err)
	}
	if stderr, err := applyGitPatch(ctx, t.Cwd.Get(), patchPath); err != nil {
		body, _ := os.ReadFile(patchPath)
		return "", fmt.Errorf("apply_diff: %w; stderr=%q hint=%q patch=%q", err, stderr,
			applyPatchFailureHint(stderr), string(body))
	}
	return "applied diff", nil
}

func applyPatchFailureHint(stderr string) string {
	if strings.Contains(stderr, "corrupt patch") || strings.Contains(stderr, "unexpected line:") || strings.Contains(stderr, "only garbage") {
		return "patch syntax is malformed — remove placeholder hunks like bare `@@`, or regenerate a complete unified diff with valid hunk headers"
	}
	if strings.Contains(stderr, "patch does not apply") || strings.Contains(stderr, "patch failed:") {
		return "patch headers were valid, but hunk context did not match the current file — re-read the target file and regenerate the diff with fresh surrounding lines"
	}
	return "context may not match the current file — re-read it and regenerate the diff"
}

func normalizeModelPatch(diff string) string {
	return diff
}

var unifiedHunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`)

// validateUnifiedDiffHunks catches malformed model-authored hunk headers before
// invoking git apply. Git's "only garbage" / "corrupt patch" diagnostics are
// technically correct but too opaque for the model to self-correct; returning a
// typed, narrow error here tells it exactly which file needs a real hunk range.
func validateUnifiedDiffHunks(diff, rel string) error {
	lines := strings.Split(diff, "\n")
	inFile := false
	seenHeader := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inFile = false
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			inFile = path == rel
		case inFile && strings.HasPrefix(line, "@@"):
			seenHeader = true
			if !unifiedHunkHeaderRE.MatchString(line) {
				return fmt.Errorf("malformed_patch: hunk header for %s must include line ranges, e.g. `@@ -12,7 +12,8 @@`; got %q", rel, line)
			}
		}
	}
	if !seenHeader {
		return fmt.Errorf("malformed_patch: diff for %s contains file headers but no hunk header", rel)
	}
	return nil
}

// applyGitPatch applies a prevalidated patch file with a strict-to-lenient
// ladder. The first pass uses --recount to repair model-authored hunk header
// arithmetic without loosening content matching; the retry only ignores
// whitespace after exact matching has already failed.
func applyGitPatch(ctx context.Context, dir, patchPath string) (string, error) {
	attempts := [][]string{
		{"apply", "--whitespace=nowarn", "--recount", patchPath},
		{"apply", "--whitespace=nowarn", "--recount", "--ignore-whitespace", patchPath},
	}
	var firstErr error
	var firstStderr string
	for _, args := range attempts {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if firstErr == nil {
				firstErr = err
				firstStderr = stderr.String()
			}
			continue
		}
		return "", nil
	}
	return firstStderr, firstErr
}

// repairFullyEscapedDiff repairs a diff that arrived fully JSON-escaped:
// a single line whose newlines were rendered as the literal 3-character
// sequence `\\n`. It only fires when the diff contains NO real newlines,
// which is the unambiguous signature of a mangled single-line diff. A
// genuine multi-line diff is returned untouched — its content lines may
// legitimately contain `\\n` (e.g. patching a `"\\n"` string/regex
// literal), and the previous unconditional ReplaceAll corrupted exactly
// those patches by splitting their content on a fabricated newline.
func repairFullyEscapedDiff(diff string) string {
	if strings.Contains(diff, "\n") {
		return diff
	}
	return strings.ReplaceAll(diff, `\\n`, "\n")
}

type ListGitChangedFilesTool struct{ Cwd *CwdRef }

func (t *ListGitChangedFilesTool) Name() string { return "list_git_changed_files" }
func (t *ListGitChangedFilesTool) Description() string {
	return "List changed files in the current git repository. Can include staged, unstaged, and optionally untracked files."
}
func (t *ListGitChangedFilesTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"staged":    map[string]any{"type": "boolean", "description": "Include staged changes (default true)"},
			"unstaged":  map[string]any{"type": "boolean", "description": "Include unstaged changes (default true)"},
			"untracked": map[string]any{"type": "boolean", "description": "Include untracked files (default true)"},
		},
	}
}
func (t *ListGitChangedFilesTool) RequiresApproval(string) bool { return false }
func (t *ListGitChangedFilesTool) ParallelSafe(string) bool     { return true }
func (t *ListGitChangedFilesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Staged    *bool `json:"staged"`
		Unstaged  *bool `json:"unstaged"`
		Untracked *bool `json:"untracked"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return "list_git_changed_files()"
}
func (t *ListGitChangedFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Staged    *bool `json:"staged"`
		Unstaged  *bool `json:"unstaged"`
		Untracked *bool `json:"untracked"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("list_git_changed_files: invalid args: %w", err)
	}
	staged := true
	unstaged := true
	untracked := true
	if a.Staged != nil {
		staged = *a.Staged
	}
	if a.Unstaged != nil {
		unstaged = *a.Unstaged
	}
	if a.Untracked != nil {
		untracked = *a.Untracked
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("list_git_changed_files: git binary not found in PATH")
	}
	seen := map[string]bool{}
	var out []string
	appendLines := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = t.Cwd.Get()
		b, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(b)))
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
		return nil
	}
	if staged {
		if err := appendLines("diff", "--name-only", "--cached"); err != nil {
			return "", fmt.Errorf("list_git_changed_files: %w", err)
		}
	}
	if unstaged {
		if err := appendLines("diff", "--name-only"); err != nil {
			return "", fmt.Errorf("list_git_changed_files: %w", err)
		}
	}
	if untracked {
		if err := appendLines("ls-files", "--others", "--exclude-standard"); err != nil {
			return "", fmt.Errorf("list_git_changed_files: %w", err)
		}
	}
	if len(out) == 0 {
		return "(no changed files)\n", nil
	}
	return strings.Join(out, "\n") + "\n", nil
}

type GitCheckpointTool struct{ Cwd *CwdRef }

func (t *GitCheckpointTool) Name() string { return "git_checkpoint" }
func (t *GitCheckpointTool) Description() string {
	return "Create a local git checkpoint commit from all current changes. Useful before risky edits so you can roll back safely."
}
func (t *GitCheckpointTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "Commit message (default: checkpoint)"},
		},
	}
}
func (t *GitCheckpointTool) RequiresApproval(string) bool { return true }
func (t *GitCheckpointTool) PreviewCall(argsJSON string) string {
	var a struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Message) == "" {
		a.Message = "checkpoint"
	}
	return fmt.Sprintf("git_checkpoint(%q)", a.Message)
}
func (t *GitCheckpointTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("git_checkpoint: invalid args: %w", err)
	}
	msg := strings.TrimSpace(a.Message)
	if msg == "" {
		msg = "checkpoint"
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git_checkpoint: git binary not found in PATH")
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = t.Cwd.Get()
		b, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git_checkpoint: git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(b)))
		}
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = t.Cwd.Get()
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git_checkpoint: rev-parse HEAD: %s", strings.TrimSpace(string(b)))
	}
	return fmt.Sprintf("created checkpoint %s", strings.TrimSpace(string(b))), nil
}

type RollbackTool struct{ Cwd *CwdRef }

func (t *RollbackTool) Name() string { return "rollback" }
func (t *RollbackTool) Description() string {
	return "Reset the git working tree to a target commit. Defaults to HEAD~1. Destructive: discards uncommitted changes."
}
func (t *RollbackTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{"type": "string", "description": "Commit or ref to reset to (default HEAD~1)"},
		},
	}
}
func (t *RollbackTool) RequiresApproval(string) bool { return true }
func (t *RollbackTool) PreviewCall(argsJSON string) string {
	var a struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Target) == "" {
		a.Target = "HEAD~1"
	}
	return fmt.Sprintf("rollback(%s)\n  $ git reset --hard %s", a.Target, a.Target)
}
func (t *RollbackTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("rollback: invalid args: %w", err)
	}
	target := strings.TrimSpace(a.Target)
	if target == "" {
		target = "HEAD~1"
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("rollback: git binary not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "git", "reset", "--hard", target)
	cmd.Dir = t.Cwd.Get()
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rollback: %s", strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

type RunTestsTool struct{ Cwd *CwdRef }

func (t *RunTestsTool) Name() string { return "run_tests" }
func (t *RunTestsTool) Description() string {
	return "Run a test command in the repository. Defaults to 'go test ./...'. Useful as a structured test/fix loop helper."
}
func (t *RunTestsTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Exact test command to run (default: go test ./...)"},
			"path":    map[string]any{"type": "string", "description": "Working directory relative to cwd (default: cwd)"},
		},
	}
}
func (t *RunTestsTool) RequiresApproval(string) bool { return true }
func (t *RunTestsTool) PreviewCall(argsJSON string) string {
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		cmd = "go test ./..."
	}
	root := a.Path
	if root == "" {
		root = "."
	}
	return fmt.Sprintf("run_tests(%s in %s)", cmd, root)
}
func (t *RunTestsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("run_tests: invalid args: %w", err)
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		command = "go test ./..."
	}
	root := t.Cwd.Get()
	if strings.TrimSpace(a.Path) != "" {
		root = a.Path
		if !filepath.IsAbs(root) {
			root = filepath.Join(t.Cwd.Get(), root)
		}
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: 1 << 20}
	cmd.Stderr = &capped{buf: &stderr, max: 1 << 20}
	runErr := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return "", fmt.Errorf("run_tests: %w", runErr)
	}
	return fmt.Sprintf("$ %s\nexit=%d\n--- stdout ---\n%s--- stderr ---\n%s", command, exit, stdout.String(), stderr.String()), nil
}
