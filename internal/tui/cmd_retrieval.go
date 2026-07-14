package tui

import (
	"context"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/memory"
)

// summaryHeading marks the start of an injected `/resume --summarized`
// or auto-summarize block. Preserved verbatim across per-turn system
// prompt rebuilds — without this dance, retrieval-driven rebuilds
// would silently delete the summary and the model would forget the
// prior session it was just told to remember.
const summaryHeading = "\n\n## Prior session context (summarized)\n"

// rebuildSystemPromptForTurn re-renders the system message using the
// per-turn retrieval orchestrator: USER.md, YOTTACODE.md, and both
// MEMORY.md indexes inject in full, while per-entry agent-managed
// memory bodies are filtered against the supplied query.
//
// Runs inside the turn goroutine (under histMu), NOT on the Update
// goroutine: the semantic strategy embeds the query via Ollama, and a
// cold model load can block for the full embed timeout — long enough
// to freeze input when this ran synchronously on Enter. ctx is the
// turn context, so Esc aborts an in-flight embed.
//
// Order of operations (startTurn appends the user message and injects
// @-file refs on the Update goroutine BEFORE the turn goroutine calls
// this):
//
//  1. Read the current system content.
//  2. Extract the "Prior session context (summarized)" block if any
//     (must survive the rebuild — that block is the only memory of
//     the prior session when --summarized was used), and the
//     "Referenced files" block startTurn just injected (it would
//     otherwise be silently dropped by the recompose, breaking the
//     @-refs of the very turn being started).
//  3. Re-Load memory from disk so mid-session edits to USER.md /
//     YOTTACODE.md and memory_save / memory_forget writes show up
//     immediately. A load error is non-fatal — we keep the existing
//     prompt so the turn can still fire.
//  4. Compose [provider hint + memory] via SystemPromptFor, filtering
//     per-entry bodies down to the relevant subset.
//  5. Re-append the preserved summary block, then the preserved
//     file-refs block (canonical order: base → memory → summary →
//     refs, matching extractSummarySection's boundary assumption).
//  6. Write back.
func (m *Model) rebuildSystemPromptForTurn(ctx context.Context, query string) {
	if len(m.sess.Messages) == 0 || m.sess.Messages[0].Role != adapter.RoleSystem {
		return
	}
	cur := m.sess.Messages[0].Content
	summary := extractSummarySection(cur)
	refsBlock := extractFileRefsBlock(cur)

	mem, err := memory.Load(m.cwd)
	if err != nil {
		return
	}
	composed := composeSystemPrompt(m.baseSystemPrompt, m.providerProfile)
	newSys := memory.SystemPromptForSemantic(ctx, composed, mem, query, m.fileCfg.Retrieval, m.embedClient)
	// Auto-recall: inject relevant excerpts from past sessions after the memory
	// tail and before the preserved summary/refs blocks, keeping the canonical
	// order base → memory → prior-convos → summary → refs. Regenerated fresh
	// each turn (like the memory tail), so it is dropped and recomputed on the
	// next rebuild rather than preserved. "" when nothing is relevant.
	if block := m.priorConversationsBlock(ctx, query); block != "" {
		newSys = strings.TrimRight(newSys, "\n") + priorConvosHeading + block
	}
	if summary != "" {
		newSys = strings.TrimRight(newSys, "\n") + summaryHeading + summary
	}
	if refsBlock != "" {
		newSys = strings.TrimRight(newSys, "\n") + "\n\n" + refsBlock
	}
	m.sess.Messages[0].Content = newSys
	// composed is the stable head; the memory tail (and any summary /
	// @-file refs appended later) follow it. Marking the head length
	// lets the Anthropic adapter cache the static prefix across turns
	// even as the tail churns. See Message.CacheHeadBytes.
	m.sess.Messages[0].CacheHeadBytes = len(composed)
}

// extractSummarySection returns the body (without heading) of the
// "## Prior session context (summarized)" block in the system prompt,
// or "" if absent. The block is bounded on the right by the next
// auto-injected section ("## Referenced files …") or end-of-string —
// both possibilities matter because filerefs may have appended after
// the summary on a prior turn.
func extractSummarySection(content string) string {
	idx := strings.Index(content, summaryHeading)
	if idx < 0 {
		return ""
	}
	body := content[idx+len(summaryHeading):]
	if r := strings.Index(body, "\n\n"+filerefs.Marker); r >= 0 {
		body = body[:r]
	}
	return strings.TrimSpace(body)
}

// extractFileRefsBlock returns the auto-injected "Referenced files"
// block (marker heading included) from the system prompt, or "" if
// absent. filerefs.Inject always appends the block as the prompt's
// tail (StripSection + append), so marker-to-end is the whole block.
func extractFileRefsBlock(content string) string {
	idx := strings.Index(content, filerefs.Marker)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(content[idx:])
}
