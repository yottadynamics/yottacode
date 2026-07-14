package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/recall"
)

// priorConvosHeading marks the auto-recall block injected into the system
// prompt each turn. Like the memory tail it is regenerated every turn from a
// fresh search (not preserved across rebuilds), so it lives after the cached
// head and its churn never invalidates the prompt cache. Distinct from
// summaryHeading so the two never collide during extraction.
const priorConvosHeading = "\n\n## Prior conversations (background — do not narrate)\n"

// embedCurrentSessionAsync embeds the current session's new or changed messages
// in the background, so the conversation just had becomes semantically
// recallable in later sessions without waiting for the next startup backfill
// (which is what makes /clear-then-continue in the same process remember what
// came before). No-op when auto-recall or embeddings are unavailable.
//
// Runs detached on its own goroutine: turn-end must never block on Ollama. It
// reads the freshly-written FTS rows from the index (not m.sess), so it does
// not race the session; writes serialize through the index's write mutex.
// Typically embeds just the turn's one or two new messages.
func (m Model) embedCurrentSessionAsync() {
	if m.recall == nil || m.embedClient == nil || m.sess == nil {
		return
	}
	if !m.fileCfg.Retrieval.SessionRecall.Auto {
		return
	}
	idx, ec, sessionID := m.recall, m.embedClient, m.sess.ID
	go func() {
		_ = idx.BackfillVectorsForSession(context.Background(), ec, ec.Model, sessionID)
	}()
}

// priorConversationsBlock semantically searches past sessions for context
// relevant to the current turn and renders a compact block for injection.
// Returns "" (inject nothing) whenever auto-recall is off, embeddings are
// unavailable, the query is empty, or nothing clears the relevance floor —
// so a turn with no relevant history adds nothing to the prompt.
//
// Reads only: it never writes memory or sessions. The embed call is bounded by
// the interactive embed timeout on m.embedClient and by ctx (the turn
// context), so a cold or hung Ollama degrades to no injection rather than
// freezing the turn.
func (m *Model) priorConversationsBlock(ctx context.Context, query string) string {
	// Reset the per-turn indicator up front so a turn that recalls nothing (or
	// where recall is unavailable) shows no "recalled N" segment.
	if m.recalledCount != nil {
		m.recalledCount.Store(0)
	}
	sr := m.fileCfg.Retrieval.SessionRecall
	if !sr.Auto || m.recall == nil || m.embedClient == nil {
		return ""
	}
	if strings.TrimSpace(query) == "" {
		return ""
	}

	queryVec, err := m.embedClient.Embed(ctx, query)
	if err != nil || len(queryVec) == 0 {
		return "" // Ollama down / timed out / empty — degrade silently.
	}

	exclude := ""
	if m.sess != nil {
		exclude = m.sess.ID
	}
	hits, err := m.recall.SearchSemantic(queryVec, recall.SemanticSearchOpts{
		Model:          m.embedClient.Model,
		Scope:          sr.Scope,
		Cwd:            m.cwd,
		ExcludeSession: exclude,
		Limit:          sr.TopK,
		MinScore:       sr.MinScore,
	})
	if err != nil || len(hits) == 0 {
		return ""
	}
	logRecallDebug(query, hits)
	if m.recalledCount != nil {
		m.recalledCount.Store(int32(len(hits)))
	}
	return renderPriorConversations(hits, sr.MaxBytes)
}

// logRecallDebug appends one line per auto-recall firing to
// ~/.yottacode/recall-debug.log when YOTTACODE_RECALL_DEBUG is set. It exists
// to calibrate min_score against real usage — the line records the query and
// each injected hit's session id and cosine score. Only injected (above-floor)
// hits are logged, so it confirms where real scores land rather than analyzing
// near-misses. Best-effort: any error is swallowed so telemetry never affects a
// turn.
func logRecallDebug(query string, hits []recall.ScoredHit) {
	if os.Getenv("YOTTACODE_RECALL_DEBUG") == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, ".yottacode", "recall-debug.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "%s query=%q hits=%d", time.Now().Format(time.RFC3339), query, len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, " [%s %.3f]", h.SessionID, h.Score)
	}
	b.WriteByte('\n')
	_, _ = f.WriteString(b.String())
}

// renderPriorConversations formats semantic hits into the injected block body
// (the text that follows priorConvosHeading). One compact line per hit,
// capped at maxBytes — but the single top hit is always admitted so a lone,
// long excerpt is never silently dropped to nothing. Pure function: unit
// tested directly. Returns "" for no hits.
func renderPriorConversations(hits []recall.ScoredHit, maxBytes int) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Relevant excerpts from earlier sessions in this project — background continuity only; do not resurface unless the user refers to them:\n")
	for i, h := range hits {
		line := priorConvoLine(h)
		if maxBytes > 0 && i > 0 && b.Len()+len(line) > maxBytes {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// priorConvoLine renders one hit as "- [date · session] role: excerpt".
func priorConvoLine(h recall.ScoredHit) string {
	name := h.SessionName
	if name == "" {
		if len(h.SessionID) >= 8 {
			name = h.SessionID[:8]
		} else {
			name = h.SessionID
		}
	}
	return fmt.Sprintf("- [%s · %s] %s: %s\n", h.Created.Format("2006-01-02"), name, h.Role, h.Snippet)
}
