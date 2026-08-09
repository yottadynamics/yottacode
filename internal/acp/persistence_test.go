package acp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// newTestMCPHTTPServer builds a real in-process MCP streamable-HTTP server
// with one read-only echo tool so ACP lifecycle tests can assert that
// session-scoped MCP clients start and stop without launching external
// binaries or touching the network.
func newTestMCPHTTPServer(t *testing.T) string {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-acp-mcp-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "echo",
		Description: "Returns the input prefixed with 'echo:'.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *sdk.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + args.Text}}}, nil, nil
	})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestCloseSession_PersistsSessionWithExchange mirrors
// internal/tui/run.go's own at-exit save gate: a session that actually
// held a conversation must be on disk (and resumable) after
// session/close.
func TestCloseSession_PersistsSessionWithExchange(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseToken("hi"), sseDone("hi")},
	})
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	loaded, err := session.Load(sessionID)
	if err != nil {
		t.Fatalf("session.Load after CloseSession: %v (session with an exchange must be persisted)", err)
	}
	if !loaded.HasExchange() {
		t.Error("persisted session has no exchange")
	}
}

// TestCloseSession_DoesNotPersistEmptySession mirrors the same gate in
// the other direction: a session/new immediately followed by
// session/close, with no prompt ever sent, must not leave a
// system-prompt-only shell in ~/.yottacode/sessions.
func TestCloseSession_DoesNotPersistEmptySession(t *testing.T) {
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
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: newResp.SessionId}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if _, err := session.Load(string(newResp.SessionId)); err == nil {
		t.Error("session with no exchange should not have been persisted, but session.Load succeeded")
	}
}

// TestCloseSession_ExportsSubagentTasks confirms the fix for the
// oneshot bug the plan flags (oneshot never exported SubagentTasks
// before saving, silently losing subagent history on resume): a
// session's live subagent registry entries must round-trip through
// CloseSession's Export + Save.
func TestCloseSession_ExportsSubagentTasks(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("hi")},
	})
	sess, _ := h.srv.session(sessionID)
	sess.rt.SubagentTasks.Add(&subagents.Task{
		ID:        "task-1",
		AgentType: "reviewer",
		Status:    subagents.TaskCompleted,
	})

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	loaded, err := session.Load(sessionID)
	if err != nil {
		t.Fatalf("session.Load: %v", err)
	}
	var found bool
	for _, tr := range loaded.SubagentTasks {
		if tr.ID == "task-1" {
			found = true
		}
	}
	if !found {
		t.Error("persisted session is missing the subagent task recorded before CloseSession")
	}
}

// TestCloseSession_ClosesRecallIndex confirms CloseSession releases the
// SQLite handle agentruntime.Builder opened for session_recall — a
// long-lived acp process handles many session/new + session/close
// cycles, and each one that leaked a handle would accumulate open
// connections to ~/.yottacode/index.sqlite for the life of the process.
// Verified behaviorally (Search fails post-close) rather than via an
// internal "closed" flag the recall package doesn't expose.
func TestCloseSession_ClosesRecallIndex(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("hi")},
	})
	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("session not registered")
	}
	if sess.rt.RecallIndex == nil {
		t.Fatal("expected rt.RecallIndex to be set for this session")
	}
	idx := sess.rt.RecallIndex

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if _, err := idx.Search("anything", 5); err == nil {
		t.Error("expected Search on the recall index to fail after CloseSession closed it")
	}
}

func TestCloseSession_StopsMCPAndLSPManagers(t *testing.T) {
	mcpURL := newTestMCPHTTPServer(t)
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	newResp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{
		Cwd: t.TempDir(),
		McpServers: []coderacp.McpServer{{Http: &coderacp.McpServerHttpInline{
			Name: "test-http",
			Url:  mcpURL,
		}}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess, ok := h.srv.session(string(newResp.SessionId))
	if !ok {
		t.Fatal("session not registered")
	}
	client := sess.rt.MCPManager.Client("test-http")
	if client == nil {
		t.Fatal("expected MCP client to be registered")
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("MCP client should be started before CloseSession: %v", err)
	}
	lspManager := sess.rt.LSPManager
	lspManager.CloseAll()

	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: newResp.SessionId}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Error("expected MCP client to be stopped after CloseSession")
	}
	if stats := lspManager.Stats(); stats.OpenServers != 0 {
		t.Errorf("expected LSP manager to have no open servers after CloseSession, got %d", stats.OpenServers)
	}
}

func TestLoadSession_ReplacesExistingLiveSessionCleanly(t *testing.T) {
	mcpURL := newTestMCPHTTPServer(t)
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{{sseDone("hi")}})
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if _, err := h.clientConn.LoadSession(ctx, coderacp.LoadSessionRequest{
		SessionId:  coderacp.SessionId(sessionID),
		Cwd:        t.TempDir(),
		McpServers: []coderacp.McpServer{{Http: &coderacp.McpServerHttpInline{Name: "test-http", Url: mcpURL}}},
	}); err != nil {
		t.Fatalf("first LoadSession: %v", err)
	}
	first, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("first loaded session not registered")
	}
	oldClient := first.rt.MCPManager.Client("test-http")
	if oldClient == nil {
		t.Fatal("expected first loaded session to start its MCP client")
	}
	if _, err := oldClient.ListTools(context.Background()); err != nil {
		t.Fatalf("MCP client should be started after first LoadSession: %v", err)
	}

	if _, err := h.clientConn.LoadSession(ctx, coderacp.LoadSessionRequest{
		SessionId:  coderacp.SessionId(sessionID),
		Cwd:        t.TempDir(),
		McpServers: []coderacp.McpServer{},
	}); err != nil {
		t.Fatalf("second LoadSession: %v", err)
	}
	if _, err := oldClient.ListTools(context.Background()); err == nil {
		t.Error("expected the previous live session's MCP client to be stopped when LoadSession replaces it")
	}
}

func TestShutdown_PersistsAllLiveSessions(t *testing.T) {
	h, sessionID := newPromptHarness(t, [][]adapter.StreamEvent{
		{sseDone("hi")},
	})
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	h.srv.Shutdown(ctx)

	if _, err := session.Load(sessionID); err != nil {
		t.Fatalf("session.Load after Shutdown: %v", err)
	}
	if _, ok := h.srv.session(sessionID); ok {
		t.Error("session still registered after Shutdown")
	}
}

// TestShutdown_RespectsCallerDeadlineAcrossManySessions is the
// regression test for the finding that Shutdown neither threaded its
// ctx into each session's drain wait nor closed sessions concurrently:
// with N sessions each stuck draining (simulating a turn that never
// finishes), the old sequential code would spend up to N *
// closeSessionDrainTimeout (5s each) regardless of the deadline
// cmd/yottacode/acp.go's caller actually gave it. Fixed by threading
// ctx into acpSession.waitForTurn and closing sessions concurrently, so
// wall time stays close to a single drain's worth even as session count
// grows, bounded by the caller's own deadline.
func TestShutdown_RespectsCallerDeadlineAcrossManySessions(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	const n = 4
	for i := range n {
		if _, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}}); err != nil {
			t.Fatalf("NewSession %d: %v", i, err)
		}
	}

	h.srv.mu.RLock()
	sessions := make([]*acpSession, 0, len(h.srv.sessions))
	for _, sess := range h.srv.sessions {
		sessions = append(sessions, sess)
	}
	h.srv.mu.RUnlock()
	if len(sessions) != n {
		t.Fatalf("expected %d live sessions, got %d", n, len(sessions))
	}
	for _, sess := range sessions {
		sess.turnWG.Add(1) // simulate a turn that never finishes draining
		t.Cleanup(sess.turnWG.Done)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer shutdownCancel()

	start := time.Now()
	h.srv.Shutdown(shutdownCtx)
	elapsed := time.Since(start)

	// Generous ceiling: well under n*closeSessionDrainTimeout (20s for
	// n=4) but well above the 200ms shutdownCtx deadline, so this only
	// fails if sessions are draining sequentially against the fixed
	// 5s-per-session ceiling instead of sharing shutdownCtx's deadline.
	const budget = 3 * time.Second
	if elapsed > budget {
		t.Errorf("Shutdown of %d stuck sessions took %v with a 200ms ctx deadline, want < %v (sequential-drain regression)", n, elapsed, budget)
	}
}
