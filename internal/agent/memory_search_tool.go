package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemorySearchTool lets the agent introspect its own memory store.
// It searches across both user and project scopes, returning ranked
// results with scores. The agent uses this to check whether a memory
// already exists before saving, to find related memories when
// reasoning about a topic, or to verify that a remembered fact is
// still current.
type MemorySearchTool struct {
	Cwd      *CwdRef
	Embedder *memory.EmbedClient
	// Strategy is the configured retrieval strategy (keyword | bm25 |
	// semantic | auto). Empty falls back to "auto" so the tool keeps
	// working when constructed without config wired. Threaded so search
	// ranks the same way injection does instead of always forcing auto.
	Strategy string
}

func (t *MemorySearchTool) Name() string { return "memory_search" }

func (t *MemorySearchTool) Description() string {
	return "Search your own memory store across both user-scope and project-scope memories. Returns ranked results with relevance scores. Use this to check if you already have a memory about a topic before saving a duplicate, to find related memories when reasoning, or to verify a remembered fact."
}

func (t *MemorySearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "natural-language search query",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "user", "project"},
				"description": "which scope to search (default: all)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "max results to return (default: 10)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *MemorySearchTool) RequiresApproval(string) bool { return false }
func (t *MemorySearchTool) ParallelSafe(string) bool     { return true }

func (t *MemorySearchTool) PreviewCall(argsJSON string) string {
	var a memorySearchArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	scope := a.Scope
	if scope == "" {
		scope = "all"
	}
	return fmt.Sprintf("memory_search(query=%q, scope=%s)", a.Query, scope)
}

type memorySearchArgs struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
	Limit int    `json:"limit"`
}

func (t *MemorySearchTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a memorySearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_search: invalid args: %w", err)
	}
	if a.Query == "" {
		return "", fmt.Errorf("memory_search: query is required")
	}
	if a.Limit <= 0 {
		a.Limit = 10
	}
	if a.Scope == "" {
		a.Scope = "all"
	}

	loaded, err := memory.Load(t.Cwd.Get())
	if err != nil {
		return "", fmt.Errorf("memory_search: load: %w", err)
	}

	var entries []memory.MemoryEntry
	switch a.Scope {
	case "user":
		entries = loaded.UserMemories
	case "project":
		entries = loaded.ProjectMemories
	case "all":
		entries = make([]memory.MemoryEntry, 0, len(loaded.UserMemories)+len(loaded.ProjectMemories))
		entries = append(entries, loaded.UserMemories...)
		entries = append(entries, loaded.ProjectMemories...)
	default:
		return "", fmt.Errorf("memory_search: invalid scope %q (want all, user, or project)", a.Scope)
	}

	if len(entries) == 0 {
		return "no memories found", nil
	}

	strategy := t.Strategy
	if strategy == "" {
		strategy = "auto"
	}
	cfg := config.RetrievalConfig{
		Enabled:  true,
		TopK:     a.Limit,
		MinScore: 0.0,
		Strategy: strategy,
	}

	scored := memory.SelectWithEmbeddingsScored(ctx, entries, a.Query, cfg, t.Embedder)

	// Drop zero-relevance entries: with MinScore=0 the selector returns
	// every memory up to the limit (score 0 included), which would label
	// irrelevant memories "matching". The CLI and TUI /memory search
	// surfaces already filter score<=0, so this keeps the agent's view
	// consistent with the operator's.
	kept := make([]memory.Scored, 0, len(scored))
	for _, s := range scored {
		if s.Score > 0 {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return "no matching memories found", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "found %d matching memories:\n", len(kept))
	for i, s := range kept {
		fmt.Fprintf(&b, "\n%d. [%s] %s (scope=%s, type=%s, score=%.3f)\n",
			i+1, s.Entry.Name, s.Entry.Description, s.Entry.Scope, s.Entry.Type, s.Score)
		fmt.Fprintf(&b, "   %s\n", truncateRunes(strings.TrimSpace(s.Entry.Body), 300))
	}
	return b.String(), nil
}

// truncateRunes caps s at max runes (not bytes) so a multibyte rune at
// the boundary is never split into invalid UTF-8 in the preview.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

var (
	_ Tool             = (*MemorySearchTool)(nil)
	_ ParallelSafeTool = (*MemorySearchTool)(nil)
)
