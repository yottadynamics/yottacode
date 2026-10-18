package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/syncutil"
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
		// channel closed — the owner sends exactly one terminal error value after
		// closing the event stream. Block here so a close/send race cannot drop a
		// panic or internal-send failure on the floor.
		return turnEndedMsg{err: <-errCh}
	}
}

// backgroundDoneInbox is a durable, session-scoped queue for completion events.
// Completion producers must never block on Bubble Tea's event loop and must not
// lose events when the model is busy in a turn or pager. The signal channel only
// wakes the waiter; the mutex-protected queue owns the actual events.
type backgroundDoneInbox struct {
	mu     syncutil.Mutex
	queue  []agent.SubagentBackgroundDone
	signal chan struct{}
}

func newBackgroundDoneInbox() *backgroundDoneInbox {
	return &backgroundDoneInbox{signal: make(chan struct{}, 1)}
}

func (q *backgroundDoneInbox) enqueue(ev agent.SubagentBackgroundDone) {
	q.mu.Lock()
	q.queue = append(q.queue, ev)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *backgroundDoneInbox) dequeue() (agent.SubagentBackgroundDone, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return agent.SubagentBackgroundDone{}, false
	}
	ev := q.queue[0]
	q.queue = q.queue[1:]
	return ev, true
}

// waitForSubagentInbox waits for a durable completion event. Re-arming after
// each event drains bursts without dropping or blocking detached producers.
func waitForSubagentInbox(q *backgroundDoneInbox) tea.Cmd {
	return func() tea.Msg {
		for {
			if ev, ok := q.dequeue(); ok {
				return agentEventMsg{ev: ev}
			}
			<-q.signal
		}
	}
}
