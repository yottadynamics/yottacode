package tui

// Proactive-memory nudges re-surface the model's memory capability during
// long sessions without adding a synthetic turn. The system prompt carries
// standing guidance, while these reminders ride ordinary user messages at
// periodic and pre-compaction checkpoints.

// preCompactionMemoryReminder is appended (with a blank line) to the
// outgoing user message of the first turn after the warn watermark is
// crossed. History-only: the transcript renders the user's own text
// without it (see startTurnWithDisplay). Worded for the message TAIL —
// it refers to "the request above".
const preCompactionMemoryReminder = "[system reminder — not from the user] Context is approaching the auto-summarize threshold; older turns will soon be compacted away. If this session has surfaced durable preferences, corrections, decisions and their rationale, gotchas, how things work, or project facts that are not yet saved, persist them with memory_save now — check the MEMORY.md indexes in your context first and update or consolidate rather than duplicate. Capture anything durable you haven't saved yet; if genuinely nothing durable is unsaved, save nothing. Either way, proceed with the user's request above without mentioning this reminder."

// captureReminderPrompt is the periodic mid-session checkpoint. Same
// mechanism as preCompactionMemoryReminder — appended to the HISTORY
// copy of an ordinary user message, never an extra turn — so it costs
// one paragraph of context and no round trip. Worded for the message
// TAIL: it refers to "the request above".
const captureReminderPrompt = "[system reminder — not from the user] Checkpoint: if this session has surfaced durable preferences, corrections, decisions and their rationale, gotchas, how things work, or project facts that are not yet saved, persist them with memory_save now — check the MEMORY.md indexes in your context first and update or consolidate rather than duplicate. Capture anything durable you haven't saved yet; if genuinely nothing durable is unsaved, save nothing. Either way, proceed with the user's request above without mentioning this reminder."

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
	if n <= 0 || m.summarizing || m.memoryNudgePending {
		return false
	}
	// userTurnsThisLaunch is incremented after this check runs, so the
	// turn being started is number userTurnsThisLaunch+1. With n=6 that
	// puts the reminder on turns 6, 12, 18…
	return (m.userTurnsThisLaunch+1)%n == 0
}
