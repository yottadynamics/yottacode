package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Proactive-memory nudges. The agent's system prompt carries the
// standing guidance on WHEN to save memories, but a static prompt loses
// the model's attention over a long agentic session — by the time
// something durable surfaces, the guidance is thousands of tokens back.
// These two nudges re-surface the capability at the exact moments the
// session context is about to be destroyed:
//
//   - preCompactionMemoryReminder rides the next user message after the
//     context watermark crosses the warn threshold, BEFORE
//     auto-summarization compacts the older turns away.
//   - exitSavePrompt runs as one final turn on a graceful quit (config
//     [memory] final_turn_on_quit, default on).
//
// Both are reminders, not extractors: the harness only picks the moment;
// whether and what to save stays the model's judgment in-band. The
// "nothing qualifies" escape hatch in each text is load-bearing — without
// it, weaker models compliance-save junk to satisfy the instruction.

// preCompactionMemoryReminder is appended (with a blank line) to the
// outgoing user message of the first turn after the warn watermark is
// crossed. History-only: the transcript renders the user's own text
// without it (see startTurnWithDisplay). Worded for the message TAIL —
// it refers to "the request above".
const preCompactionMemoryReminder = "[system reminder — not from the user] Context is approaching the auto-summarize threshold; older turns will soon be compacted away. If this session has surfaced durable user preferences, corrections, validated approaches, or project facts that are not yet saved, persist them with memory_save now — check the MEMORY.md indexes in your context first and update or consolidate rather than duplicate. If nothing durable is unsaved, save nothing. Either way, proceed with the user's request above without mentioning this reminder."

// exitSaveMinUserTurns is the minimum number of user turns STARTED THIS
// LAUNCH (resumed history doesn't count — it already had its own exit
// pass) before a graceful quit runs the final memory turn. Below the
// bar, quit stays instant: zero- and one-turn sessions rarely teach
// anything durable, and an exit delay on trivial sessions is how a
// feature like this gets switched off.
const exitSaveMinUserTurns = 2

// exitSavePrompt is the synthetic user message driving the final turn on
// quit. Memory tools only — the session is ending and file mutations at
// quit time would be unreviewable surprise.
const exitSavePrompt = "The session is ending now. Review this conversation: if it surfaced durable user preferences, corrections, validated approaches, or project facts that are not yet in your memory, persist them with memory_save before the context is gone. Check the MEMORY.md indexes in your context first — update or consolidate existing memories rather than creating near-duplicates, and memory_forget any now known to be stale. Use memory tools only; do not modify project files. If nothing durable is unsaved, reply exactly: nothing to save."

// exitSaveDisplayLabel is what the transcript shows instead of the full
// exitSavePrompt body.
const exitSaveDisplayLabel = "[memory] final pass — saving durable memories before exit (Esc to skip)"

// maybeStartExitSaveTurn either starts the final memory turn (the quit
// completes when it ends — see the exitSavePending branch of
// turnEndedMsg) or quits immediately when the final turn isn't
// warranted: feature off, no adapter, summarization in flight, an
// exit-save already running, or a session below the activity bar.
//
// Only graceful, deliberate exits route here (/quit and Ctrl+D while
// idle). Ctrl+C always quits immediately — it's the "get me out now"
// gesture and overloading it with a model call would betray that. While
// the final turn runs, Esc/Ctrl+C cancels it (normal turn-interrupt
// path) and the quit then completes; Ctrl+D hard-quits as ever.
func maybeStartExitSaveTurn(m Model) (tea.Model, tea.Cmd) {
	if !m.fileCfg.Memory.FinalTurnOnQuit ||
		m.cfg.Adapter == nil ||
		m.turnActive ||
		m.summarizing ||
		m.exitSavePending ||
		m.userTurnsThisLaunch < exitSaveMinUserTurns {
		return m, tea.Quit
	}
	m.exitSavePending = true
	// The exit prompt already asks for a full save pass; a pending
	// pre-compaction reminder riding the same message would just repeat
	// it.
	m.memoryNudgePending = false
	return m.startTurnWithDisplay(exitSavePrompt, exitSaveDisplayLabel)
}
