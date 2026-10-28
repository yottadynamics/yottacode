package acp

import (
	"context"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

func TestSessionOutboundSequencesAndFansOutUpdates(t *testing.T) {
	controller := newRecordingOutbound()
	observer := newRecordingOutbound()
	sess := &acpSession{id: "session-1", controller: controller, observers: []outbound{observer}}
	routing := sess.routing()
	update := coderacp.UpdateAgentMessageText("hello")
	if err := routing.SessionUpdate(context.Background(), coderacp.SessionNotification{SessionId: "session-1", Update: update}); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}
	if got := sess.latestSeq(); got != 1 {
		t.Fatalf("latest sequence = %d, want 1", got)
	}
	if len(controller.updates) != 1 || len(observer.updates) != 1 {
		t.Fatalf("fanout controller=%d observer=%d", len(controller.updates), len(observer.updates))
	}
}

func TestSessionReplayEventsAfterCursor(t *testing.T) {
	sess := &acpSession{id: "session-1"}
	sess.appendEvent(coderacp.UpdateAgentMessageText("one"))
	sess.appendEvent(coderacp.UpdateAgentMessageText("two"))
	out := newRecordingOutbound()
	count, latest, err := sess.replayEvents(context.Background(), out, 1)
	if err != nil {
		t.Fatalf("replayEvents: %v", err)
	}
	if count != 1 || latest != 2 {
		t.Fatalf("count=%d latest=%d", count, latest)
	}
	if len(out.updates) != 1 || out.updates[0].Update.AgentMessageChunk == nil {
		t.Fatalf("unexpected replay updates: %+v", out.updates)
	}
}

type recordingOutbound struct {
	updates []coderacp.SessionNotification
}

func newRecordingOutbound() *recordingOutbound { return &recordingOutbound{} }
func (r *recordingOutbound) SessionUpdate(_ context.Context, n coderacp.SessionNotification) error {
	r.updates = append(r.updates, n)
	return nil
}
func (r *recordingOutbound) RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	return coderacp.RequestPermissionResponse{}, nil
}
