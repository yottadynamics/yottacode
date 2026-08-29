package acp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// scriptedStreamer is a duplicate of the one in internal/oneshot (and
// internal/agent) — kept here per this repo's convention of small
// private duplicates over a shared testutil package (see
// internal/oneshot/oneshot_test.go's own comment on the same trade-off).
type scriptedStreamer struct {
	turns [][]adapter.StreamEvent
	mu    sync.Mutex
	next  int
}

func (s *scriptedStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.mu.Lock()
	out := make(chan adapter.StreamEvent, 16)
	if s.next >= len(s.turns) {
		s.mu.Unlock()
		close(out)
		return out
	}
	turn := s.turns[s.next]
	s.next++
	s.mu.Unlock()
	go func() {
		defer close(out)
		for _, ev := range turn {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}

func sseToken(s string) adapter.StreamEvent {
	return adapter.StreamEvent{Kind: adapter.EventTokenDelta, Token: s}
}
func sseDone(content string, tcs ...adapter.ToolCall) adapter.StreamEvent {
	msg := &adapter.Message{Role: adapter.RoleAssistant, Content: content, ToolCalls: tcs}
	return adapter.StreamEvent{Kind: adapter.EventDone, Final: msg}
}

// fakeApprovalTool is a duplicate of internal/oneshot's test double of
// the same name — a Tool that always requires approval.
type fakeApprovalTool struct{ name string }

func (m *fakeApprovalTool) Name() string                 { return m.name }
func (m *fakeApprovalTool) Description() string          { return "fake" }
func (m *fakeApprovalTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (m *fakeApprovalTool) RequiresApproval(string) bool { return true }
func (m *fakeApprovalTool) PreviewCall(string) string    { return m.name + "()" }
func (m *fakeApprovalTool) Execute(_ context.Context, _ string) (string, error) {
	return "tool ran", nil
}

// newPromptHarness builds a harness with a real registered session (via
// the wire protocol, exercising the same NewSession path production
// code takes) and then swaps the session's adapter for a scripted fake
// — the only piece of a real turn that must stay fake in a unit test.
func newPromptHarness(t *testing.T, turns [][]adapter.StreamEvent) (*testHarness, string) {
	t.Helper()
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	newResp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionID := string(newResp.SessionId)

	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatalf("session %s not registered after NewSession", sessionID)
	}
	sess.rt.Cfg.Adapter = &scriptedStreamer{turns: turns}
	return h, sessionID
}

func TestPrompt_ContentStreamsToClientAsAgentMessageChunks(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseToken("Hel"), sseToken("lo"), sseDone("Hello")},
	})
	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != coderacp.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, coderacp.StopReasonEndTurn)
	}

	var text strings.Builder
	for _, u := range h.client.Updates() {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			text.WriteString(u.Update.AgentMessageChunk.Content.Text.Text)
		}
	}
	if got := text.String(); got != "Hello" {
		t.Errorf("streamed content = %q, want %q", got, "Hello")
	}
}

// TestPrompt_StampsUserMessageTimestamp confirms the ACP submit site (the
// one RoleUser append not covered by the shared agent-loop stampNow choke
// point) sets Timestamp to the actual submit time.
func TestPrompt_StampsUserMessageTimestamp(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("Hello")},
	})
	ctx, cancel := withTimeout(t)
	defer cancel()

	before := time.Now()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	after := time.Now()

	sess, _ := h.srv.session(sessionID)
	var userMsg *adapter.Message
	for i := range sess.rt.Session.Messages {
		if sess.rt.Session.Messages[i].Role == adapter.RoleUser {
			userMsg = &sess.rt.Session.Messages[i]
		}
	}
	if userMsg == nil {
		t.Fatal("expected a RoleUser message in the session")
	}
	if userMsg.Timestamp == nil {
		t.Fatal("expected the submitted user message to carry a Timestamp")
	}
	if userMsg.Timestamp.Before(before) || userMsg.Timestamp.After(after) {
		t.Errorf("Timestamp %v not within [%v, %v]", userMsg.Timestamp, before, after)
	}
}

func TestPrompt_ApprovalNeeded_AllowOnceLetsToolRun(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
		{sseDone("done")},
	})
	sess, _ := h.srv.session(sessionID)
	sess.rt.Registry.Register(&fakeApprovalTool{name: "danger"})
	h.client.permissionResponse = coderacp.RequestPermissionResponse{
		Outcome: coderacp.RequestPermissionOutcome{
			Selected: &coderacp.RequestPermissionOutcomeSelected{OptionId: "allow_once"},
		},
	}

	ctx, cancel := withTimeout(t)
	defer cancel()
	resp, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != coderacp.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, coderacp.StopReasonEndTurn)
	}

	var sawCompletedToolCall bool
	for _, u := range h.client.Updates() {
		if u.Update.ToolCallUpdate != nil && u.Update.ToolCallUpdate.Status != nil && *u.Update.ToolCallUpdate.Status == coderacp.ToolCallStatusCompleted {
			sawCompletedToolCall = true
		}
	}
	if !sawCompletedToolCall {
		t.Error("expected a completed tool_call_update after allow_once")
	}
}

func TestPrompt_ApprovalNeeded_OffersRejectOnce(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
		{sseDone("done")},
	})
	sess, _ := h.srv.session(sessionID)
	sess.rt.Registry.Register(&fakeApprovalTool{name: "danger"})

	var capturedOptions []coderacp.PermissionOption
	h.client.onRequestPermission = func(req coderacp.RequestPermissionRequest) {
		capturedOptions = req.Options
	}
	h.client.permissionResponse = coderacp.RequestPermissionResponse{
		Outcome: coderacp.RequestPermissionOutcome{
			Selected: &coderacp.RequestPermissionOutcomeSelected{OptionId: "reject_once"},
		},
	}

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var haveRejectOnce bool
	for _, o := range capturedOptions {
		if o.Kind == coderacp.PermissionOptionKindRejectOnce {
			haveRejectOnce = true
		}
	}
	if !haveRejectOnce {
		t.Error("permission request must always offer a reject_once option (clients match on kind, never a hardcoded optionId)")
	}
}

func TestPrompt_UnknownSessionIdErrors(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: "does-not-exist",
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

// blockingStreamer never sends a StreamEvent until its context is
// cancelled — the fixture for proving session/cancel actually reaches
// and interrupts an in-flight turn, not just that Prompt returns
// eventually.
type blockingStreamer struct {
	started chan struct{}
	once    sync.Once
}

func (s *blockingStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent)
	go func() {
		defer close(out)
		// The ACP runtime can retry or re-enter ChatStream for the same
		// fixture while cancelling an in-flight prompt. Signal the first
		// start only; closing started twice would panic and hide the
		// cancellation behavior this test is meant to verify.
		s.once.Do(func() { close(s.started) })
		<-ctx.Done()
	}()
	return out
}

func TestCancel_InterruptsInFlightPrompt(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	blocker := &blockingStreamer{started: make(chan struct{})}
	sess.rt.Cfg.Adapter = blocker

	ctx, cancel := withTimeout(t)
	defer cancel()

	promptDone := make(chan coderacp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
			SessionId: coderacp.SessionId(sessionID),
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
		})
		promptErr <- err
		promptDone <- resp
	}()

	select {
	case <-blocker.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the turn to start")
	}
	if err := h.clientConn.Cancel(ctx, coderacp.CancelNotification{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt returned an error after cancel: %v", err)
		}
		resp := <-promptDone
		if resp.StopReason != coderacp.StopReasonCancelled {
			t.Errorf("StopReason = %q, want %q", resp.StopReason, coderacp.StopReasonCancelled)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Prompt to return after Cancel")
	}
}

func TestCancel_InterruptsPendingPermissionRequest(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
	})
	sess, _ := h.srv.session(sessionID)
	sess.rt.Registry.Register(&fakeApprovalTool{name: "danger"})

	permissionRequested := make(chan struct{})
	h.client.blockPermissionUntilContextDone = true
	h.client.onRequestPermission = func(coderacp.RequestPermissionRequest) {
		close(permissionRequested)
	}

	promptCtx, cancelPrompt := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelPrompt()
	promptDone := make(chan coderacp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := h.clientConn.Prompt(promptCtx, coderacp.PromptRequest{
			SessionId: coderacp.SessionId(sessionID),
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
		})
		promptErr <- err
		promptDone <- resp
	}()

	select {
	case <-permissionRequested:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for the permission request")
	}
	if err := h.clientConn.Cancel(context.Background(), coderacp.CancelNotification{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt returned an error after cancel: %v", err)
		}
		resp := <-promptDone
		if resp.StopReason != coderacp.StopReasonCancelled {
			t.Errorf("StopReason = %q, want %q", resp.StopReason, coderacp.StopReasonCancelled)
		}
	case <-time.After(500 * time.Millisecond):
		cancelPrompt()
		t.Fatal("Prompt stayed blocked in session/request_permission after Cancel")
	}
}

// TestPrompt_ConcurrentPromptForSameSessionIsRejected is the regression
// test for a bug where nothing prevented two concurrent session/prompt
// calls for the same session from racing appends to the same
// rt.Session.Messages slice — coder/acp-go-sdk dispatches every inbound
// request in its own goroutine (see connection.go's receive loop), so
// the ACP spec's assumption that a client waits for a PromptResponse
// before sending another isn't transport-enforced. The second prompt
// while the first is still in flight must be rejected outright, not
// silently interleaved with it.
func TestPrompt_ConcurrentPromptForSameSessionIsRejected(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	blocker := &blockingStreamer{started: make(chan struct{})}
	sess.rt.Cfg.Adapter = blocker

	ctx, cancel := withTimeout(t)
	defer cancel()

	promptDone := make(chan coderacp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
			SessionId: coderacp.SessionId(sessionID),
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("first")},
		})
		promptErr <- err
		promptDone <- resp
	}()

	select {
	case <-blocker.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the first turn to start")
	}

	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("second")},
	}); err == nil {
		t.Fatal("expected the second concurrent Prompt call to be rejected")
	}

	if err := h.clientConn.Cancel(ctx, coderacp.CancelNotification{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("first Prompt returned an error after cancel: %v", err)
		}
		<-promptDone
	case <-ctx.Done():
		t.Fatal("timed out waiting for the first Prompt to return after Cancel")
	}
}
