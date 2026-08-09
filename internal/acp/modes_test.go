package acp

import (
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

func newModeTestSession(t *testing.T) (*testHarness, string) {
	t.Helper()
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if resp.Modes == nil {
		t.Fatal("NewSessionResponse.Modes is nil — session modes must be advertised")
	}
	if resp.Modes.CurrentModeId != modeAsk {
		t.Errorf("initial CurrentModeId = %q, want %q", resp.Modes.CurrentModeId, modeAsk)
	}
	return h, string(resp.SessionId)
}

// TestSetSessionMode_EachNamedModeIsExclusive locks in the mutual
// exclusion policy ported from internal/tui/cmd_auto.go and cmd_plan.go:
// picking Architect or Code clears the other two named states (and
// Yolo), and picking Ask clears everything.
func TestSetSessionMode_EachNamedModeIsExclusive(t *testing.T) {
	cases := []struct {
		name     string
		id       coderacp.SessionModeId
		wantPlan bool
		wantAuto bool
		wantYolo bool
	}{
		{"architect", modeArchitect, true, false, false},
		{"code", modeCode, false, true, false},
		{"ask", modeAsk, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, sessionID := newModeTestSession(t)
			sess, _ := h.srv.session(sessionID)
			// Start from a dirty state (all three on) to prove the
			// transition actually clears what it should, not just sets
			// what it should.
			sess.rt.PlanMode.Active.Store(true)
			sess.rt.AutoMode.Active.Store(true)
			sess.rt.YoloMode.Active.Store(true)

			ctx, cancel := withTimeout(t)
			defer cancel()
			resp, err := h.clientConn.SetSessionMode(ctx, coderacp.SetSessionModeRequest{
				SessionId: coderacp.SessionId(sessionID),
				ModeId:    tc.id,
			})
			if err != nil {
				t.Fatalf("SetSessionMode: %v", err)
			}
			_ = resp

			if got := sess.rt.PlanMode.IsActive(); got != tc.wantPlan {
				t.Errorf("PlanMode.IsActive() = %v, want %v", got, tc.wantPlan)
			}
			if got := sess.rt.AutoMode.IsActive(); got != tc.wantAuto {
				t.Errorf("AutoMode.IsActive() = %v, want %v", got, tc.wantAuto)
			}
			if got := sess.rt.YoloMode.IsActive(); got != tc.wantYolo {
				t.Errorf("YoloMode.IsActive() = %v, want %v", got, tc.wantYolo)
			}
		})
	}
}

// TestSetSessionMode_YoloIsAdditive matches internal/tui/cmd_yolo.go's
// enterYoloMode: entering Yolo must NOT clear whichever named mode was
// already active underneath it.
func TestSetSessionMode_YoloIsAdditive(t *testing.T) {
	h, sessionID := newModeTestSession(t)
	sess, _ := h.srv.session(sessionID)
	sess.rt.AutoMode.Active.Store(true)

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.SetSessionMode(ctx, coderacp.SetSessionModeRequest{
		SessionId: coderacp.SessionId(sessionID),
		ModeId:    modeYolo,
	}); err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}

	if !sess.rt.YoloMode.IsActive() {
		t.Error("YoloMode should be active after selecting yolo")
	}
	if !sess.rt.AutoMode.IsActive() {
		t.Error("AutoMode should stay active — yolo is an additive overlay, not a replacement")
	}
	if currentModeID(sess.rt) != modeYolo {
		t.Errorf("currentModeID() = %q, want %q (yolo takes display precedence)", currentModeID(sess.rt), modeYolo)
	}
}

func TestSetSessionMode_UnknownSessionErrors(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := h.clientConn.SetSessionMode(ctx, coderacp.SetSessionModeRequest{
		SessionId: "does-not-exist",
		ModeId:    modeCode,
	}); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

func TestSetSessionMode_RejectsWhileTurnInProgress(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	blocker := &blockingStreamer{started: make(chan struct{})}
	sess.rt.Cfg.Adapter = blocker

	ctx, cancel := withTimeout(t)
	defer cancel()
	promptErr := make(chan error, 1)
	go func() {
		_, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
			SessionId: coderacp.SessionId(sessionID),
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("first")},
		})
		promptErr <- err
	}()

	select {
	case <-blocker.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the turn to start")
	}
	if _, err := h.clientConn.SetSessionMode(ctx, coderacp.SetSessionModeRequest{
		SessionId: coderacp.SessionId(sessionID),
		ModeId:    modeCode,
	}); err == nil {
		t.Fatal("expected SetSessionMode to reject changes while a turn is in progress")
	}
	if err := h.clientConn.Cancel(ctx, coderacp.CancelNotification{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt returned an error after cancel: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Prompt to return after Cancel")
	}
}
