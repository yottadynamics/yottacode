package tui

import (
	"testing"
	"time"
)

// TestStartTurn_StampsUserMessageTimestamp confirms the TUI's own submit
// site (the one RoleUser append not covered by the shared agent-loop
// stampNow choke point) sets Timestamp to the actual submit time.
func TestStartTurn_StampsUserMessageTimestamp(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}

	before := time.Now()
	out, _ := m.startTurn("hello there")
	after := time.Now()
	m2 := out.(Model)
	defer m2.turnCancel()

	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if last.Timestamp == nil {
		t.Fatal("expected the submitted user message to carry a Timestamp")
	}
	if last.Timestamp.Before(before) || last.Timestamp.After(after) {
		t.Errorf("Timestamp %v not within [%v, %v]", last.Timestamp, before, after)
	}
}
