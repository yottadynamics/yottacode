package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemoryCurateApplyTool performs narrow, approval-gated memory curation
// actions that are safe to apply mechanically after memory_audit has surfaced
// them. It deliberately avoids subjective rewrites or merges: those still need
// the agent to read, propose, and save explicit final content.
type MemoryCurateApplyTool struct {
	Cwd *CwdRef
}

func (t *MemoryCurateApplyTool) Name() string { return "memory_curate_apply" }

func (t *MemoryCurateApplyTool) Description() string {
	return "Apply one simple memory_audit curation issue after user approval. Supports only mechanical cases: delete empty-body memories, or move portable-in-project user/feedback memories to user scope when the target does not already exist. Does not rewrite, merge, or auto-promote notes."
}

func (t *MemoryCurateApplyTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"problem": map[string]any{
				"type":        "string",
				"enum":        []string{"empty-body", "portable-in-project"},
				"description": "the audit problem to apply (must match memory_audit output)",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"user", "project"},
				"description": "the current scope of the memory from memory_audit",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "the memory name from memory_audit",
			},
		},
		"required": []string{"problem", "scope", "name"},
	}
}

func (t *MemoryCurateApplyTool) RequiresApproval(string) bool { return true }
func (t *MemoryCurateApplyTool) ParallelSafe(string) bool     { return false }

func (t *MemoryCurateApplyTool) PreviewCall(argsJSON string) string {
	var a memoryCurateApplyArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	switch a.Problem {
	case "empty-body":
		return fmt.Sprintf("memory_curate_apply(delete empty %s/%s)", a.Scope, a.Name)
	case "portable-in-project":
		return fmt.Sprintf("memory_curate_apply(move project/%s to user scope)", a.Name)
	default:
		return fmt.Sprintf("memory_curate_apply(problem=%s, scope=%s, name=%s)", a.Problem, a.Scope, a.Name)
	}
}

type memoryCurateApplyArgs struct {
	Problem string `json:"problem"`
	Scope   string `json:"scope"`
	Name    string `json:"name"`
}

func (t *MemoryCurateApplyTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memoryCurateApplyArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_curate_apply: invalid args: %w", err)
	}
	if err := validateCurateApplyArgs(a); err != nil {
		return "", err
	}
	loaded, err := memory.Load(t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("memory_curate_apply: load: %w", err)
	}
	if !auditHasIssue(memory.Audit(loaded), a.Problem, a.Scope, a.Name) {
		return "", fmt.Errorf("memory_curate_apply: %s/%s no longer has audit issue %q", a.Scope, a.Name, a.Problem)
	}
	switch a.Problem {
	case "empty-body":
		return t.applyDeleteEmpty(a)
	case "portable-in-project":
		return t.applyMovePortable(a)
	default:
		return "", fmt.Errorf("memory_curate_apply: unsupported problem %q", a.Problem)
	}
}

func validateCurateApplyArgs(a memoryCurateApplyArgs) error {
	if a.Scope != "user" && a.Scope != "project" {
		return fmt.Errorf("memory_curate_apply: invalid scope %q (want user or project)", a.Scope)
	}
	switch a.Problem {
	case "empty-body", "portable-in-project":
		return nil
	default:
		return fmt.Errorf("memory_curate_apply: unsupported problem %q", a.Problem)
	}
}

func auditHasIssue(report memory.AuditReport, problem, scope, name string) bool {
	for _, issue := range report.Issues {
		if issue.Problem == problem && issue.Scope == scope && issue.Name == name {
			return true
		}
	}
	return false
}

func (t *MemoryCurateApplyTool) applyDeleteEmpty(a memoryCurateApplyArgs) (string, error) {
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd.Get())
	if err != nil {
		return "", err
	}
	unlock := lockMemoryPath(path)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("memory_curate_apply: read %q: %w", path, err)
	}
	_, body, ok := memory.ParseFrontmatter(data)
	if !ok || strings.TrimSpace(body) != "" {
		return "", fmt.Errorf("memory_curate_apply: %s/%s is no longer empty", a.Scope, a.Name)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("memory_curate_apply: remove %q: %w", path, err)
	}
	memory.DeleteVec(path)
	if err := memory.RegenerateMemoryIndex(a.Scope, t.Cwd.Get()); err != nil {
		return "", fmt.Errorf("memory_curate_apply: regenerate index: %w", err)
	}
	return fmt.Sprintf("deleted empty %s memory %q", a.Scope, a.Name), nil
}

func (t *MemoryCurateApplyTool) applyMovePortable(a memoryCurateApplyArgs) (string, error) {
	if a.Scope != "project" {
		return "", fmt.Errorf("memory_curate_apply: portable-in-project applies only to project-scope memories")
	}
	cwd := t.Cwd.Get()
	projectPath, err := memory.MemoryFilePath("project", a.Name, cwd)
	if err != nil {
		return "", err
	}
	userPath, err := memory.MemoryFilePath("user", a.Name, cwd)
	if err != nil {
		return "", err
	}
	unlock := lockMemoryPaths(projectPath, userPath)
	defer unlock()
	if _, err := os.Stat(userPath); err == nil {
		return "", fmt.Errorf("memory_curate_apply: user memory %q already exists; merge manually with memory_get/memory_save", a.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("memory_curate_apply: stat %q: %w", userPath, err)
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		return "", fmt.Errorf("memory_curate_apply: read %q: %w", projectPath, err)
	}
	fm, _, ok := memory.ParseFrontmatter(data)
	if !ok || (fm.Type != "user" && fm.Type != "feedback") {
		return "", fmt.Errorf("memory_curate_apply: project/%s is no longer a portable user/feedback memory", a.Name)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		return "", fmt.Errorf("memory_curate_apply: mkdir %q: %w", filepath.Dir(userPath), err)
	}
	if err := memory.AtomicWrite(userPath, data, 0o600); err != nil {
		return "", fmt.Errorf("memory_curate_apply: write %q: %w", userPath, err)
	}
	copyMemoryVec(projectPath, userPath)
	if err := os.Remove(projectPath); err != nil {
		return "", fmt.Errorf("memory_curate_apply: remove %q after copy: %w", projectPath, err)
	}
	memory.DeleteVec(projectPath)
	if err := memory.RegenerateMemoryIndex("user", cwd); err != nil {
		return "", fmt.Errorf("memory_curate_apply: regenerate user index: %w", err)
	}
	if err := memory.RegenerateMemoryIndex("project", cwd); err != nil {
		return "", fmt.Errorf("memory_curate_apply: regenerate project index: %w", err)
	}
	return fmt.Sprintf("moved project memory %q to user scope", a.Name), nil
}

func lockMemoryPaths(paths ...string) func() {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	unlocks := make([]func(), 0, len(paths))
	for _, path := range paths {
		unlocks = append(unlocks, lockMemoryPath(path))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

func copyMemoryVec(srcMemoryPath, dstMemoryPath string) {
	srcVec := memory.VecPath(srcMemoryPath)
	data, err := os.ReadFile(srcVec)
	if err != nil {
		return
	}
	_ = os.WriteFile(memory.VecPath(dstMemoryPath), data, 0o600)
}

var _ Tool = (*MemoryCurateApplyTool)(nil)
