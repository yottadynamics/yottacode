package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
)

// A deep-research run needs no model turn at either end. The workflow is
// deterministic Go, its summary is already verified and citation-checked, and
// the only thing a relaying model could add is cost (a full re-read of the
// conversation per turn) and uncited commentary. So the TUI starts the run
// itself (cmdDeepResearch) and, when it finishes, prints the summary itself
// (deliverDeepResearch) — recording both ends in history as a user/assistant
// pair, the same shape the model-driven flow leaves, so follow-up questions
// see the report and providers see nothing new.

// appendSyntheticExchange records a user/assistant pair in the conversation
// history without a model turn. Only call it while idle: mid-turn the agent
// loop owns the history, and inserting between a tool call and its result
// would break the message sequence.
func (m *Model) appendSyntheticExchange(userContent, assistantContent string) {
	now := time.Now()
	m.histMu.Lock()
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: userContent, Timestamp: &now},
		adapter.Message{Role: adapter.RoleAssistant, Content: assistantContent, Timestamp: &now},
	)
	m.histMu.Unlock()
}

// persistSyntheticExchange does what the turn-end path does for a normal turn:
// refresh the context estimate, then save the session and index it for recall.
func (m *Model) persistSyntheticExchange() {
	m.refreshContextTokens()
	if m.subagentTasks != nil {
		m.sess.SubagentTasks = m.subagentTasks.Export()
	}
	if err := m.sess.Save(); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("⚠ session save failed: %v", err)))
	}
	if m.recall != nil {
		if err := m.recall.IndexSession(m.sess); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("⚠ recall index failed: %v", err)))
		}
	}
}

// emitAssistantText prints text into scrollback the way streamed assistant
// prose is printed, one line at a time.
func (m *Model) emitAssistantText(text string) {
	m.appendLine("")
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		m.emitAssistantProse(line)
	}
}

// deliverDeepResearch shows a finished (or failed or stopped) run's summary in
// scrollback and records it in history. Idle-only, like every caller of
// startSubagentWakeTurn.
func (m *Model) deliverDeepResearch(w agent.SubagentBackgroundDone) {
	result := w.Result
	if strings.TrimSpace(result) == "" {
		// Never record an empty assistant message: providers reject it, which
		// would fail every later turn of the session.
		result = "Deep research finished without a result."
	}
	m.emitAssistantText(result)
	m.appendSyntheticExchange(
		fmt.Sprintf("[Background task finished: %s %s]", w.AgentType, shortTaskID(w.TaskID)),
		result,
	)
	m.persistSyntheticExchange()
}

// startDeepResearchDirect starts a run from the slash command without a model
// turn. It reports handled=false when the model-driven path should be used
// instead (plan mode, which must keep blocking the tool; or a session that
// cannot host a detached run).
func (m *Model) startDeepResearchDirect(input string, breadth int, query string) (handled bool) {
	if m.cfg.PlanMode.IsActive() || m.cfg.Registry == nil {
		return false
	}
	t, ok := m.cfg.Registry.Get(agent.DeepResearchToolName)
	tool, isTool := t.(*agent.DeepResearchTool)
	if !ok || !isTool {
		return false
	}
	taskID, err := tool.StartBackground(query, breadth)
	if err != nil {
		if errors.Is(err, agent.ErrDeepResearchForeground) {
			return false
		}
		m.appendLine(styleError.Render("[deep-research] " + err.Error()))
		return true
	}
	// A /checkpoints entry for the command, taken before the exchange is
	// appended (restore needs the conversation as it stood when you typed it),
	// exactly as a normal turn does. Soft on failure: a checkpoint-store error
	// must not undo a run that has already started. It restores the
	// conversation only — the run itself is detached and the report file it
	// writes is not tracked.
	if store, ok := m.cfg.Checkpoints.(*checkpoint.Store); ok && store != nil {
		_, _ = store.Begin(m.sess.ID, input, len(m.sess.Messages), m.sess.Messages)
	}
	m.enteredConversation = true
	m.firstMessageSent = true
	m.appendLine(renderUserBlock(input, m.width))
	m.recordHistory(input)
	started := fmt.Sprintf("Deep research started in the background (task %s). "+
		"Progress shows in the dock; the report arrives here when it finishes.", shortTaskID(taskID))
	m.emitAssistantText(started)
	m.appendSyntheticExchange(input, started)
	m.persistSyntheticExchange()
	return true
}
