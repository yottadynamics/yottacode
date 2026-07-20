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
// "nothing to save" escape hatch in each text is load-bearing — without
// it, weaker models compliance-save junk to satisfy the instruction. The
// wording still biases toward recall: capture anything durable you have
// not saved yet, and use the escape hatch only when there is genuinely
// nothing durable to persist.

// preCompactionMemoryReminder is appended (with a blank line) to the
// outgoing user message of the first turn after the warn watermark is
// crossed. History-only: the transcript renders the user's own text
// without it (see startTurnWithDisplay). Worded for the message TAIL —
// it refers to "the request above".
const preCompactionMemoryReminder = "[system reminder — not from the user] Context is approaching the auto-summarize threshold; older turns will soon be compacted away. If this session has surfaced durable preferences, corrections, decisions and their rationale, gotchas, how things work, or project facts that are not yet saved, persist them with memory_save now — check the MEMORY.md indexes in your context first and update or consolidate rather than duplicate. Capture anything durable you haven't saved yet; if genuinely nothing durable is unsaved, save nothing. Either way, proceed with the user's request above without mentioning this reminder."

// exitSaveMinUserTurns is the minimum number of user turns STARTED THIS
// LAUNCH (resumed history doesn't count — it already had its own exit
// pass) before a graceful quit runs the final memory turn. A zero-turn
// session still skips it: there is nothing to review, and an exit delay
// on a session you opened and closed is how a feature like this gets
// switched off.
//
// One turn is enough. The bar used to be two, on the theory that short
// sessions rarely teach anything durable — but a single exchange
// routinely carries a correction or a decision-and-why, and under the
// recall bias (see prompt.go) those are exactly what should be captured.
// Silently skipping the pass on every one-turn session dropped them.
const exitSaveMinUserTurns = 1

// exitSavePrompt is the synthetic user message driving the final turn on
// quit. Memory tools only — the session is ending and file mutations at
// quit time would be unreviewable surprise.
const exitSavePrompt = "The session is ending now. Review this conversation: if it surfaced durable preferences, corrections, decisions and their rationale, gotchas, how things work, or project facts that are not yet in your memory, persist them with memory_save before the context is gone. Check the MEMORY.md indexes in your context first — update or consolidate existing memories rather than creating near-duplicates, and memory_forget any now known to be stale. Use memory tools only; do not modify project files. Capture anything durable you haven't saved yet. If genuinely nothing durable is unsaved, reply exactly: nothing to save."

// captureReminderPrompt is the periodic mid-session checkpoint. Same
// mechanism as preCompactionMemoryReminder — appended to the HISTORY
// copy of an ordinary user message, never an extra turn — so it costs
// one paragraph of context and no round trip. Worded for the message
// TAIL: it refers to "the request above".
const captureReminderPrompt = "[system reminder — not from the user] Checkpoint: if anything durable has surfaced since your last save — a decision and why, a gotcha, how something works, a preference or correction — and it isn't in memory yet, persist it with memory_save now (check your MEMORY.md indexes first; update or consolidate rather than duplicate). If nothing durable is unsaved, save nothing. Either way, proceed with the request above without mentioning this reminder."

// captureReminderDue reports whether this turn should carry the periodic
// capture reminder.
//
// Why this exists: the other two reinforcement points both have narrow
// triggers. preCompactionMemoryReminder only fires if a session crosses
// the summarize watermark, which most never do, and the exit-save turn
// needs a graceful /quit — so a medium session ended with Ctrl+C got
// ZERO reinforcement beyond the standing prompt it stopped attending to
// thousands of tokens ago. This covers those sessions mid-flight.
//
// Deliberately NOT a per-turn reminder (that variant was considered and
// rejected as too noisy): it fires on a turn cadence, defaults to every
// 6th turn, and rides an existing message rather than adding a turn.
//
// Suppressed while summarizing or during the exit-save turn, and it
// yields to a pending pre-compaction reminder — that one is strictly
// more urgent (context is about to be destroyed) and asks for the same
// thing, so doubling up would just repeat itself.
func (m Model) captureReminderDue() bool {
	n := m.fileCfg.Memory.CaptureReminderEveryTurns
	if n <= 0 || m.summarizing || m.exitSavePending || m.memoryNudgePending {
		return false
	}
	// userTurnsThisLaunch is incremented after this check runs, so the
	// turn being started is number userTurnsThisLaunch+1. With n=6 that
	// puts the reminder on turns 6, 12, 18…
	return (m.userTurnsThisLaunch+1)%n == 0
}

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
