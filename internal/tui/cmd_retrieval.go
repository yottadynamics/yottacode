package tui

import (
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
// Order of operations (matches startTurn):
//
//  1. Read the current system content.
//  2. Extract the "Prior session context (summarized)" block if any
//     (must survive the rebuild — that block is the only memory of
//     the prior session when --summarized was used).
//  3. Re-Load memory from disk so mid-session edits to USER.md /
//     YOTTACODE.md and memory_save / memory_forget writes show up
//     immediately. A load error is non-fatal — we keep the existing
//     prompt so the turn can still fire.
//  4. Compose [provider hint + memory] via SystemPromptFor, filtering
//     per-entry bodies down to the relevant subset.
//  5. Re-append the preserved summary block, if any.
//  6. Write back. The downstream filerefs.Inject pass in startTurn
//     handles the @-file references on its own — we deliberately
//     don't include them here.
func (m *Model) rebuildSystemPromptForTurn(query string) {
	if len(m.sess.Messages) == 0 || m.sess.Messages[0].Role != adapter.RoleSystem {
		return
	}
	cur := m.sess.Messages[0].Content
	summary := extractSummarySection(cur)

	mem, err := memory.Load(m.cwd)
	if err != nil {
		return
	}
	composed := composeSystemPrompt(m.baseSystemPrompt, m.providerProfile)
	newSys := memory.SystemPromptForSemantic(composed, mem, query, m.fileCfg.Retrieval, m.embedClient)
	if summary != "" {
		newSys = strings.TrimRight(newSys, "\n") + summaryHeading + summary
	}
	m.sess.Messages[0].Content = newSys
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
