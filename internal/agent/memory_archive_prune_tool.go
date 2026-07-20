package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemoryArchivePruneTool inventories or prunes archived prior memory versions.
// Dry runs are read-only; actual deletion is approval-gated and constrained to
// files discovered under memory .archive directories.
type MemoryArchivePruneTool struct {
	Cwd *CwdRef
}

func (t *MemoryArchivePruneTool) Name() string { return "memory_archive_prune" }

func (t *MemoryArchivePruneTool) Description() string {
	return "List or prune archived prior memory versions under .archive directories. Dry runs are read-only; actual prune deletes only selected archive files and requires approval. Never deletes live memory files."
}

func (t *MemoryArchivePruneTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "user", "project"},
				"description": "which memory scope to inspect/prune (default: all)",
			},
			"older_than_days": map[string]any{
				"type":        "integer",
				"description": "select archives older than this many days; 0 means no age cutoff",
			},
			"keep_latest": map[string]any{
				"type":        "integer",
				"description": "keep this many newest archives per memory; 0 means keep none by count",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "if true, report what would be deleted without deleting files (default: true)",
			},
		},
	}
}

func (t *MemoryArchivePruneTool) RequiresApproval(argsJSON string) bool {
	a := parseMemoryArchivePruneArgs(argsJSON)
	return !a.DryRun
}
func (t *MemoryArchivePruneTool) ParallelSafe(argsJSON string) bool {
	return !t.RequiresApproval(argsJSON)
}

func (t *MemoryArchivePruneTool) PreviewCall(argsJSON string) string {
	a := parseMemoryArchivePruneArgs(argsJSON)
	if a.DryRun {
		return fmt.Sprintf("memory_archive_prune(dry-run scope=%s older_than_days=%d keep_latest=%d)", a.Scope, a.OlderThanDays, a.KeepLatest)
	}
	return fmt.Sprintf("memory_archive_prune(DELETE archives scope=%s older_than_days=%d keep_latest=%d)", a.Scope, a.OlderThanDays, a.KeepLatest)
}

type memoryArchivePruneArgs struct {
	Scope         string `json:"scope"`
	OlderThanDays int    `json:"older_than_days"`
	KeepLatest    int    `json:"keep_latest"`
	DryRun        bool   `json:"dry_run"`
}

func parseMemoryArchivePruneArgs(argsJSON string) memoryArchivePruneArgs {
	var a memoryArchivePruneArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Scope == "" {
		a.Scope = "all"
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(argsJSON), &raw)
	if _, ok := raw["dry_run"]; !ok {
		a.DryRun = true
	}
	return a
}

func (t *MemoryArchivePruneTool) Execute(_ context.Context, argsJSON string) (string, error) {
	a := parseMemoryArchivePruneArgs(argsJSON)
	if err := json.Unmarshal([]byte(argsJSON), &memoryArchivePruneArgs{}); err != nil {
		return "", fmt.Errorf("memory_archive_prune: invalid args: %w", err)
	}
	if a.OlderThanDays <= 0 && a.KeepLatest <= 0 {
		return "", fmt.Errorf("memory_archive_prune: requires older_than_days or keep_latest")
	}
	res, err := memory.PruneArchives(t.Cwd.Get(), memory.ArchivePruneOptions{Scope: a.Scope, OlderThanDays: a.OlderThanDays, KeepLatest: a.KeepLatest, DryRun: a.DryRun})
	if err != nil {
		return "", err
	}
	return renderMemoryArchivePruneResult(res, a.DryRun), nil
}

func renderMemoryArchivePruneResult(res memory.ArchivePruneResult, dryRun bool) string {
	verb := "would delete"
	if !dryRun {
		verb = "deleted"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d archive file(s), %d bytes", verb, res.Matched, res.Bytes)
	for _, e := range res.Entries {
		fmt.Fprintf(&b, "\n- %s/%s %s %d bytes", e.Scope, e.Memory, memory.FormatArchiveTime(e.ModTime), e.Size)
	}
	return b.String()
}

var (
	_ Tool             = (*MemoryArchivePruneTool)(nil)
	_ ParallelSafeTool = (*MemoryArchivePruneTool)(nil)
)
