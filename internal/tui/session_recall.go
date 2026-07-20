package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// top_k = 0 means inject nothing. Enforced here rather than left to
	// SearchSemantic, whose non-positive-limit default is 10 — a sensible
	// library default, but as a *config* value it made `top_k = 0` inject
	// MORE than the default of 3, which is the opposite of what the knob
	// reads like.
	if sr.TopK <= 0 {
		return ""
	}
	// Sensitive project: never inject automatically. Checked before the embed
	// so a quarantined repo does no recall work at all. The manual
	// session_recall tool is unaffected — the gate is about what leaves on its
	// own, not about making your own history unreachable when you ask for it.
	if m.sensitiveProject {
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
	opts := recall.SemanticSearchOpts{
		Model:          m.embedClient.Model,
		Scope:          sr.Scope,
		Cwd:            m.cwd,
		ProjectRoots:   m.projectRoots,
		ExcludeRoots:   m.sensitiveRoots,
		ExcludeSession: exclude,
		Limit:          sr.TopK,
		MinScore:       sr.MinScore,
	}
	// Under YOTTACODE_RECALL_DEBUG, search without the floor and with a wider
	// cap so the log can show what *nearly* made it, then re-apply the real
	// gate in Go. applyRecallGate is behaviour-identical to the filtering
	// SearchSemantic would have done, so the injected block is the same either
	// way — telemetry observes the decision, it never changes it. With the env
	// var unset none of this runs and the query is exactly as before.
	debug := recallDebugEnabled()
	if debug {
		opts.MinScore = 0
		opts.Limit = max(opts.Limit, recallDebugLimit)
	}
	hits, err := m.recall.SearchSemantic(queryVec, opts)
	if err != nil {
		return ""
	}
	if debug {
		gated := applyRecallGate(hits, sr.MinScore, sr.TopK)
		logRecallCandidates(query, hits, len(gated), sr.MinScore)
		hits = gated
	}
	if len(hits) == 0 {
		return ""
	}
	if m.recalledCount != nil {
		m.recalledCount.Store(int32(len(hits)))
	}
	return renderPriorConversations(hits, sr.MaxBytes)
}

// recallDebugLimit is how many candidates the YOTTACODE_RECALL_DEBUG pass pulls
// back so near-misses are visible. Wide enough to show what sat just under the
// floor, and never narrower than the configured top_k, so the Go-side gate
// applied afterwards still sees everything the ungated search would have.
const recallDebugLimit = 20

// recallDebugEnabled reports whether near-miss telemetry is on.
func recallDebugEnabled() bool { return os.Getenv("YOTTACODE_RECALL_DEBUG") != "" }

// applyRecallGate reproduces the floor-and-cap the search would have applied,
// for the debug path where it deliberately ran without them. What gets
// injected must not depend on whether telemetry is switched on.
//
// A non-positive topK yields nothing, matching the config-level meaning of
// top_k = 0 that priorConversationsBlock enforces — NOT SearchSemantic's
// library-level "unspecified limit defaults to 10".
//
// hits arrive sorted by score descending, so the floor keeps a prefix and the
// cap keeps a prefix of that. That is what lets logRecallCandidates mark hit i
// as injected purely by index.
func applyRecallGate(hits []recall.ScoredHit, minScore float64, topK int) []recall.ScoredHit {
	if topK <= 0 {
		return nil
	}
	out := hits
	for i, h := range hits {
		if h.Score < minScore {
			out = hits[:i]
			break
		}
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

// logRecallCandidates appends one line per auto-recall search to
// ~/.yottacode/recall-debug.log when YOTTACODE_RECALL_DEBUG is set. It exists
// to calibrate min_score against real usage, so it records *every* candidate the
// search returned — including the ones the floor dropped, and including turns
// where nothing scored at all. Logging only the injected hits could never
// answer "is 0.6 too high?", because the hits that would answer it are exactly
// the ones that never appear.
//
// The raw query is deliberately not written: debug logs live on disk after the
// turn, and user prompts can contain secrets or PHI. A short digest keeps lines
// correlatable while avoiding a second copy of sensitive text.
//
// injected is the count applyRecallGate kept; since candidates are sorted
// descending and the gate keeps a prefix, index < injected is the marker.
// Best-effort: every error is swallowed so telemetry never affects a turn.
func logRecallCandidates(query string, candidates []recall.ScoredHit, injected int, minScore float64) {
	if !recallDebugEnabled() {
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
	fmt.Fprintf(&b, "%s query_sha256=%s candidates=%d injected=%d min_score=%.3f",
		time.Now().Format(time.RFC3339), recallQueryDigest(query), len(candidates), injected, minScore)
	for i, h := range candidates {
		state := "dropped"
		if i < injected {
			state = "injected"
		}
		fmt.Fprintf(&b, " [%s %.3f %s]", h.SessionID, h.Score, state)
	}
	b.WriteByte('\n')
	_, _ = f.WriteString(b.String())
}

// recallQueryDigest returns a short stable digest for debug-log correlation
// without persisting the raw user prompt.
func recallQueryDigest(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])[:16]
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
