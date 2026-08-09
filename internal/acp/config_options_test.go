package acp

import (
	"testing"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/cli"
)

func TestNewSession_AdvertisesEffortConfigOption(t *testing.T) {
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

	if len(resp.ConfigOptions) != 1 {
		t.Fatalf("ConfigOptions = %d entries, want 1 (effort only — no router pair configured for this session)", len(resp.ConfigOptions))
	}
	opt := resp.ConfigOptions[0]
	if opt.Select == nil {
		t.Fatalf("effort option is not a Select: %+v", opt)
	}
	if opt.Select.Id != configIdEffort {
		t.Errorf("Select.Id = %q, want %q", opt.Select.Id, configIdEffort)
	}
	if opt.Select.CurrentValue != "default" {
		t.Errorf("Select.CurrentValue = %q, want \"default\"", opt.Select.CurrentValue)
	}
	if opt.Select.Options.Ungrouped == nil || len(*opt.Select.Options.Ungrouped) != 4 {
		t.Errorf("expected 4 ungrouped effort options, got %+v", opt.Select.Options)
	}
}

func TestSetSessionConfigOption_Effort_Success(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption: %v", err)
	}
	if len(resp.ConfigOptions) != 1 || resp.ConfigOptions[0].Select == nil {
		t.Fatalf("unexpected response shape: %+v", resp.ConfigOptions)
	}
	if resp.ConfigOptions[0].Select.CurrentValue != "high" {
		t.Errorf("CurrentValue = %q, want \"high\"", resp.ConfigOptions[0].Select.CurrentValue)
	}

	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("session not registered")
	}
	if sess.rt.ChatOptions.ReasoningEffort != "high" {
		t.Errorf("rt.ChatOptions.ReasoningEffort = %q, want \"high\"", sess.rt.ChatOptions.ReasoningEffort)
	}
}

func TestSetSessionConfigOption_Effort_UnknownValueErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "extreme",
		},
	})
	if err == nil {
		t.Error("expected an error for an unrecognized effort value")
	}
}

func TestSetSessionConfigOption_Effort_WrongVariantErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     true,
		},
	})
	if err == nil {
		t.Error("expected an error sending a boolean value for the effort (select-typed) option")
	}
}

func TestSetSessionConfigOption_UnknownConfigIdErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  "does-not-exist",
			Value:     "high",
		},
	})
	if err == nil {
		t.Error("expected an error for an unknown config option id")
	}
}

func TestSetSessionConfigOption_UnknownSessionErrors(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: "does-not-exist",
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	})
	if err == nil {
		t.Error("expected an error for an unknown session id")
	}
}

// TestSetSessionConfigOption_Advisor_NoPairErrors and
// TestSetSessionConfigOption_Advisor_TogglesWithPair construct
// sess.rt.RouterAdapters directly (rt.Adapter, already a real client from
// the test harness's stub-backed Build call, satisfies adapter.Client) —
// agentruntime's own tests already cover the harder router-interaction
// correctness (effort rebuild keeping a routed session on the advisor
// model, etc.); these only need to prove the RPC dispatches correctly.

func TestSetSessionConfigOption_Advisor_NoPairErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdAdvisor,
			Value:     true,
		},
	})
	if err == nil {
		t.Error("expected an error enabling advisor routing with no configured pair")
	}
}

func TestSetSessionConfigOption_Advisor_TogglesWithPair(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("session not registered")
	}
	sess.rt.RouterAdapters = &cli.RouterAdapters{
		Advisor:          sess.rt.Adapter,
		Implementer:      sess.rt.Adapter,
		AdvisorModel:     "advisor-model",
		ImplementerModel: "implementer-model",
	}

	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdAdvisor,
			Value:     true,
		},
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption(advisor=true): %v", err)
	}
	var found bool
	for _, opt := range resp.ConfigOptions {
		if opt.Boolean != nil && opt.Boolean.Id == configIdAdvisor {
			found = true
			if !opt.Boolean.CurrentValue {
				t.Error("advisor CurrentValue should be true after enabling")
			}
		}
	}
	if !found {
		t.Fatal("response ConfigOptions missing the advisor boolean entry")
	}
	if !sess.rt.AgentTool.RouteAuto {
		t.Error("rt.AgentTool.RouteAuto should be true after enabling advisor routing")
	}
}

func TestSetSessionConfigOption_RejectedWhileTurnInFlight(t *testing.T) {
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
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
		})
		promptErr <- err
	}()

	select {
	case <-blocker.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the turn to start")
	}

	if _, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	}); err == nil {
		t.Error("expected SetSessionConfigOption to be rejected while a turn is in flight")
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
		t.Fatal("timed out waiting for the Prompt to return after Cancel")
	}
}
