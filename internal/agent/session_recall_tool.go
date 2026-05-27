package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RecallHit mirrors recall.Hit without importing the recall package
// (which would create an import cycle via session → agent).
type RecallHit struct {
	SessionID   string
	SessionName string
	Model       string
	Created     time.Time
	Role        string
	Snippet     string
}

// RecallSearcher is the interface the session_recall tool needs from
// the recall index. Satisfied by *recall.Index — wired in run.go via
// a thin adapter so the agent package stays cycle-free.
type RecallSearcher interface {
	Search(query string, limit int) ([]RecallHit, error)
}

// SessionRecallTool lets the agent search across past sessions via
// the FTS5 index. The agent uses this to pull in context from prior
// conversations — "I think we discussed authentication before" — or
// to check whether an issue was already resolved in a previous
// session, without the user having to invoke /recall manually.
type SessionRecallTool struct {
	Searcher RecallSearcher
}

func (t *SessionRecallTool) Name() string { return "session_recall" }

func (t *SessionRecallTool) Description() string {
	return "Search across all past sessions for prior discussions, decisions, or context. Returns ranked snippets with session metadata. Use this when you suspect a topic was discussed before, when you want to check if an issue was already resolved, or when prior context would help you make a better decision now."
}

func (t *SessionRecallTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "natural-language search query (FTS5 syntax also accepted: OR, quotes for exact phrases)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "max results to return (default: 10)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *SessionRecallTool) RequiresApproval(string) bool { return false }
func (t *SessionRecallTool) ParallelSafe(string) bool     { return true }

func (t *SessionRecallTool) PreviewCall(argsJSON string) string {
	var a sessionRecallArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("session_recall(query=%q)", a.Query)
}

type sessionRecallArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *SessionRecallTool) Execute(_ context.Context, argsJSON string) (string, error) {
	if t.Searcher == nil {
		return "", fmt.Errorf("session_recall: recall index not available (session indexing may be disabled)")
	}
	var a sessionRecallArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("session_recall: invalid args: %w", err)
	}
	if a.Query == "" {
		return "", fmt.Errorf("session_recall: query is required")
	}
	if a.Limit <= 0 {
		a.Limit = 10
	}

	hits, err := t.Searcher.Search(a.Query, a.Limit)
	if err != nil {
		return "", fmt.Errorf("session_recall: search: %w", err)
	}

	if len(hits) == 0 {
		return "no matching sessions found", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "found %d results across past sessions:\n", len(hits))
	for i, h := range hits {
		name := h.SessionName
		if name == "" && len(h.SessionID) >= 8 {
			name = h.SessionID[:8]
		}
		date := h.Created.Format(time.DateOnly)
		fmt.Fprintf(&b, "\n%d. session %q (%s, %s) [%s]:\n   %s\n",
			i+1, name, date, h.Model, h.Role, h.Snippet)
	}
	return b.String(), nil
}

var (
	_ Tool             = (*SessionRecallTool)(nil)
	_ ParallelSafeTool = (*SessionRecallTool)(nil)
)
