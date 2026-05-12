package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Bubbletea wraps each input from outside the program (keystroke, agent event)
// in a tea.Msg. We define our own messages for the agent → UI bridge.

// agentEventMsg carries one Event off the events channel into the Update loop.
type agentEventMsg struct{ ev agent.Event }

// turnEndedMsg fires when the agent goroutine finishes and the events channel
// is closed. Distinct from the per-event messages so the model can re-enable
// input.
type turnEndedMsg struct{ err error }

// waitForEvent returns a tea.Cmd that blocks on the next event from ch and
// surfaces it as a tea.Msg. When the channel closes it surfaces a
// turnEndedMsg. After handling each agentEventMsg, the model issues another
// waitForEvent — this is the standard Bubbletea pattern for streaming sources.
func waitForEvent(ch <-chan agent.Event, errCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if ok {
			return agentEventMsg{ev: ev}
		}
		// channel closed — surface the turn's terminal error if any
		select {
		case err := <-errCh:
			return turnEndedMsg{err: err}
		default:
			return turnEndedMsg{}
		}
	}
}

// waitForSubagentInbox returns a tea.Cmd that blocks on the long-lived
// subagent inbox. Background subagents emit SubagentBackgroundDone here
// after the parent turn has already ended; the cmd surfaces them as a
// normal agentEventMsg so handleAgentEvent renders the completion card.
// The model re-arms this cmd after each message lands so the inbox
// drains continuously for the life of the program.
func waitForSubagentInbox(ch <-chan agent.SubagentBackgroundDone) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return agentEventMsg{ev: ev}
	}
}
