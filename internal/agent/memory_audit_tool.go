package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemoryAuditTool lets the agent inspect memory-store hygiene before doing a
// curation pass. It is intentionally read-only: the agent must explicitly use
// memory_get, memory_save, and memory_forget for each consolidation decision so
// memory changes stay reviewable in the transcript.
type MemoryAuditTool struct {
	Cwd *CwdRef
}

func (t *MemoryAuditTool) Name() string { return "memory_audit" }

func (t *MemoryAuditTool) Description() string {
	return "Read-only audit of your memory store for curation opportunities: quick-capture notes, duplicate descriptions, empty or description-only bodies, and portable user/feedback memories saved to project scope. Use before memory curation passes, then apply fixes explicitly with memory_get, memory_save, and memory_forget."
}

func (t *MemoryAuditTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "user", "project"},
				"description": "which memory scope to audit (default: all)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "max issues to return (default: 50)",
			},
			"plan": map[string]any{
				"type":        "boolean",
				"description": "return a grouped read-only curation plan instead of the issue list",
			},
		},
	}
}

func (t *MemoryAuditTool) RequiresApproval(string) bool { return false }
func (t *MemoryAuditTool) ParallelSafe(string) bool     { return true }

func (t *MemoryAuditTool) PreviewCall(argsJSON string) string {
	var a memoryAuditArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	scope := a.Scope
	if scope == "" {
		scope = "all"
	}
	return fmt.Sprintf("memory_audit(scope=%s, plan=%t)", scope, a.Plan)
}

type memoryAuditArgs struct {
	Scope string `json:"scope"`
	Limit int    `json:"limit"`
	Plan  bool   `json:"plan"`
}

func (t *MemoryAuditTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memoryAuditArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_audit: invalid args: %w", err)
	}
	if a.Scope == "" {
		a.Scope = "all"
	}
	if a.Limit <= 0 {
		a.Limit = 50
	}

	loaded, err := memory.Load(t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("memory_audit: load: %w", err)
	}
	loaded, err = filterAuditScope(loaded, a.Scope)
	if err != nil {
		return "", err
	}
	report := memory.Audit(loaded)
	if a.Plan {
		return renderMemoryAuditPlan(report), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "memories: %d total, %d quick note(s), %d issue(s)\n", report.Total, report.QuickNotes, len(report.Issues))
	if len(report.Issues) == 0 {
		b.WriteString("memory store looks curated")
		return b.String(), nil
	}
	if len(report.Issues) > a.Limit {
		fmt.Fprintf(&b, "showing first %d issue(s)\n", a.Limit)
	}
	maxIssues := len(report.Issues)
	if maxIssues > a.Limit {
		maxIssues = a.Limit
	}
	for i := 0; i < maxIssues; i++ {
		issue := report.Issues[i]
		fmt.Fprintf(&b, "\n%d. %s/%s [%s] %s\n   created: %s (%s old)\n   detail: %s\n   action: %s", i+1, issue.Scope, issue.Name, issue.Type, issue.Problem, memory.FormatAuditCreated(issue.Created), memory.FormatAuditAge(issue.AgeDays), issue.Detail, issue.Action)
	}
	return b.String(), nil
}

func renderMemoryAuditPlan(report memory.AuditReport) string {
	plan := memory.PlanCuration(report)
	var b strings.Builder
	fmt.Fprintf(&b, "curation plan: %d issue(s), %d batch(es)\n", plan.TotalIssues, len(plan.Batches))
	if len(plan.Batches) == 0 {
		b.WriteString("memory store looks curated")
		return b.String()
	}
	for i, batch := range plan.Batches {
		fmt.Fprintf(&b, "\n%d. %s (%d issue(s))\n", i+1, batch.Title, len(batch.Issues))
		fmt.Fprintf(&b, "   action: %s", batch.Action)
		for _, issue := range batch.Issues {
			fmt.Fprintf(&b, "\n   - %s/%s [%s] %s, created %s (%s old)",
				issue.Scope, issue.Name, issue.Type, issue.Problem,
				memory.FormatAuditCreated(issue.Created), memory.FormatAuditAge(issue.AgeDays))
		}
	}
	return b.String()
}

func filterAuditScope(loaded memory.Loaded, scope string) (memory.Loaded, error) {
	switch scope {
	case "all":
		return loaded, nil
	case "user":
		loaded.ProjectMemories = nil
		return loaded, nil
	case "project":
		loaded.UserMemories = nil
		return loaded, nil
	default:
		return memory.Loaded{}, fmt.Errorf("memory_audit: invalid scope %q (want all, user, or project)", scope)
	}
}

var (
	_ Tool             = (*MemoryAuditTool)(nil)
	_ ParallelSafeTool = (*MemoryAuditTool)(nil)
)
