package agent

import (
	"context"
	"fmt"
	"strings"
)

// PRReadinessContextTool gathers a cheap local readiness snapshot before a PR.
type PRReadinessContextTool struct{ Cwd *CwdRef }

func (t *PRReadinessContextTool) Name() string { return "pr_readiness_context" }
func (t *PRReadinessContextTool) Description() string {
	return "Read-only PR readiness snapshot: branch, changed files, docs/tests hints, and local git state. Use before opening or updating a PR."
}
func (t *PRReadinessContextTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *PRReadinessContextTool) RequiresApproval(string) bool { return false }
func (t *PRReadinessContextTool) ParallelSafe(string) bool     { return true }
func (t *PRReadinessContextTool) PreviewCall(string) string    { return "pr_readiness_context()" }
func (t *PRReadinessContextTool) Execute(ctx context.Context, _ string) (string, error) {
	cwd := "."
	if t.Cwd != nil {
		cwd = t.Cwd.Get()
	}
	branch, _ := gitOutput(ctx, cwd, "branch", "--show-current")
	status, _ := gitOutput(ctx, cwd, "status", "--short")
	files, _ := gitOutput(ctx, cwd, "diff", "--name-only", "HEAD")
	if strings.TrimSpace(files) == "" {
		files, _ = gitOutput(ctx, cwd, "diff", "--cached", "--name-only")
	}
	var docs, tests bool
	for _, f := range strings.Fields(files) {
		if strings.HasPrefix(f, "docs/") || strings.EqualFold(f, "README.md") || strings.HasSuffix(f, ".md") {
			docs = true
		}
		if strings.HasSuffix(f, "_test.go") {
			tests = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "branch: %s\n", emptyAs(branch, "unknown"))
	fmt.Fprintf(&b, "dirty: %s\n", yesNo(strings.TrimSpace(status) != ""))
	fmt.Fprintf(&b, "changed_files:\n%s\n", indentOrNone(files))
	fmt.Fprintf(&b, "docs_touched: %s\n", yesNo(docs))
	fmt.Fprintf(&b, "tests_touched: %s\n", yesNo(tests))
	if !docs {
		b.WriteString("hint: no docs changed; confirm this is not user-facing behavior\n")
	}
	if !tests {
		b.WriteString("hint: no tests changed; bug fixes/features usually need regression coverage\n")
	}
	return b.String(), nil
}

func indentOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "  (none)"
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		lines = append(lines, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func emptyAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
