package tui

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestBackgroundDoneInboxRetainsBursts(t *testing.T) {
	q := newBackgroundDoneInbox()
	for i := range 128 {
		q.enqueue(agent.SubagentBackgroundDone{TaskID: string(rune('a' + i))})
	}
	for i := range 128 {
		ev, ok := q.dequeue()
		if !ok {
			t.Fatalf("event %d was lost", i)
		}
		if ev.TaskID != string(rune('a'+i)) {
			t.Fatalf("event %d = %q", i, ev.TaskID)
		}
	}
	if _, ok := q.dequeue(); ok {
		t.Fatal("queue should be empty after draining")
	}
}

func TestBackgroundDoneInboxSignalWakesWaiter(t *testing.T) {
	q := newBackgroundDoneInbox()
	q.enqueue(agent.SubagentBackgroundDone{TaskID: "done"})
	msg := waitForSubagentInbox(q)()
	e, ok := msg.(agentEventMsg)
	if !ok {
		t.Fatalf("message = %#v", msg)
	}
	done, ok := e.ev.(agent.SubagentBackgroundDone)
	if !ok || done.TaskID != "done" {
		t.Fatalf("event = %#v", e.ev)
	}
}
