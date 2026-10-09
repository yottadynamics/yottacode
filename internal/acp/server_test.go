package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/cli"
)

// discardLogger silences coder/acp-go-sdk's own connection-lifecycle
// logging (e.g. "connection closed" when a test's pipe cleanup runs) so
// `go test -v` output stays readable.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeClient is a minimal coderacp.Client implementation for in-process
// pipe tests — the realistic substitute for live Zed/JetBrains
// conformance testing this pass cannot perform (see
// roadmap/acp-adapter.md). Records session/update notifications and
// permission requests so later milestones (M6/M7) can assert on them;
// M5 only needs it to satisfy the interface.
type fakeClient struct {
	mu      chan struct{} // 1-buffered mutex substitute avoiding an import just for sync
	updates []coderacp.SessionNotification
	// permissionResponse is returned verbatim from RequestPermission when
	// set; the zero value (Cancelled) is returned otherwise.
	permissionResponse coderacp.RequestPermissionResponse
	// blockPermissionUntilContextDone makes RequestPermission wait for the
	// caller's context to cancel, letting tests prove cancellation is wired to
	// the permission round trip rather than only to the agent turn.
	blockPermissionUntilContextDone bool
	// onRequestPermission, when set, is called synchronously with each
	// RequestPermission call's params before permissionResponse is
	// returned — lets a test inspect what was asked (e.g. which
	// PermissionOptionKinds were offered) without racing the updates
	// slice.
	onRequestPermission func(coderacp.RequestPermissionRequest)
}

func newFakeClient() *fakeClient {
	c := &fakeClient{mu: make(chan struct{}, 1)}
	c.mu <- struct{}{}
	return c
}

func (c *fakeClient) lock()   { <-c.mu }
func (c *fakeClient) unlock() { c.mu <- struct{}{} }

func (c *fakeClient) Updates() []coderacp.SessionNotification {
	c.lock()
	defer c.unlock()
	return append([]coderacp.SessionNotification(nil), c.updates...)
}

var _ coderacp.Client = (*fakeClient)(nil)

func (c *fakeClient) ReadTextFile(context.Context, coderacp.ReadTextFileRequest) (coderacp.ReadTextFileResponse, error) {
	return coderacp.ReadTextFileResponse{}, coderacp.NewMethodNotFound("fs/read_text_file")
}

func (c *fakeClient) WriteTextFile(context.Context, coderacp.WriteTextFileRequest) (coderacp.WriteTextFileResponse, error) {
	return coderacp.WriteTextFileResponse{}, coderacp.NewMethodNotFound("fs/write_text_file")
}

func (c *fakeClient) RequestPermission(ctx context.Context, params coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	c.lock()
	resp := c.permissionResponse
	block := c.blockPermissionUntilContextDone
	hook := c.onRequestPermission
	c.unlock()
	if hook != nil {
		hook(params)
	}
	if block {
		<-ctx.Done()
		return coderacp.RequestPermissionResponse{}, ctx.Err()
	}
	return resp, nil
}

func (c *fakeClient) SessionUpdate(_ context.Context, params coderacp.SessionNotification) error {
	c.lock()
	c.updates = append(c.updates, params)
	c.unlock()
	return nil
}

func (c *fakeClient) CreateTerminal(context.Context, coderacp.CreateTerminalRequest) (coderacp.CreateTerminalResponse, error) {
	return coderacp.CreateTerminalResponse{}, coderacp.NewMethodNotFound("terminal/create")
}

func (c *fakeClient) KillTerminal(context.Context, coderacp.KillTerminalRequest) (coderacp.KillTerminalResponse, error) {
	return coderacp.KillTerminalResponse{}, coderacp.NewMethodNotFound("terminal/kill")
}

func (c *fakeClient) TerminalOutput(context.Context, coderacp.TerminalOutputRequest) (coderacp.TerminalOutputResponse, error) {
	return coderacp.TerminalOutputResponse{}, coderacp.NewMethodNotFound("terminal/output")
}

func (c *fakeClient) ReleaseTerminal(context.Context, coderacp.ReleaseTerminalRequest) (coderacp.ReleaseTerminalResponse, error) {
	return coderacp.ReleaseTerminalResponse{}, coderacp.NewMethodNotFound("terminal/release")
}

func (c *fakeClient) WaitForTerminalExit(context.Context, coderacp.WaitForTerminalExitRequest) (coderacp.WaitForTerminalExitResponse, error) {
	return coderacp.WaitForTerminalExitResponse{}, coderacp.NewMethodNotFound("terminal/wait_for_exit")
}

// testHarness wires a real Server to a fakeClient over two io.Pipes, the
// same shape a real editor's stdio connection to `yottacode acp` has.
type testHarness struct {
	srv        *Server
	agentConn  *coderacp.AgentSideConnection
	clientConn *coderacp.ClientSideConnection
	client     *fakeClient
}

// newTestOpts mirrors internal/agentruntime's newTestSpec helper: an
// isolated HOME plus a stub OpenAI-compatible /models endpoint so
// Build's preflight probe succeeds without real network access.
func newTestOpts(t *testing.T) cli.ChatOptions {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
	}))
	t.Cleanup(srv.Close)

	return cli.ChatOptions{
		Model:         "test-model",
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		ProviderKind:  "openai",
		MaxIterations: 10,
	}
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	srv := NewServer(newTestOpts(t))
	client := newFakeClient()

	serverReader, clientWriter := io.Pipe() // client writes -> server reads
	clientReader, serverWriter := io.Pipe() // server writes -> client reads
	t.Cleanup(func() {
		serverReader.Close()
		clientWriter.Close()
		clientReader.Close()
		serverWriter.Close()
	})

	agentConn := coderacp.NewAgentSideConnection(srv, serverWriter, serverReader)
	agentConn.SetLogger(discardLogger)
	srv.SetConnection(agentConn)
	clientConn := coderacp.NewClientSideConnection(client, clientWriter, clientReader)
	clientConn.SetLogger(discardLogger)

	return &testHarness{srv: srv, agentConn: agentConn, clientConn: clientConn, client: client}
}

func withTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestServer_InitializeNegotiatesProtocolVersion(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{
		ProtocolVersion: coderacp.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if resp.ProtocolVersion != coderacp.ProtocolVersionNumber {
		t.Errorf("ProtocolVersion = %v, want %v", resp.ProtocolVersion, coderacp.ProtocolVersionNumber)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Error("AgentCapabilities.LoadSession = false, want true (session/load must be advertised)")
	}
	if !resp.AgentCapabilities.McpCapabilities.Http || !resp.AgentCapabilities.McpCapabilities.Sse {
		t.Errorf("AgentCapabilities.McpCapabilities = %+v, want Http=true Sse=true (a spec-compliant client won't send http/sse McpServer entries otherwise)", resp.AgentCapabilities.McpCapabilities)
	}
}

func TestServer_NewSessionReturnsSessionId(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	cwd := t.TempDir()
	resp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: cwd, McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if resp.SessionId == "" {
		t.Fatal("NewSession returned an empty SessionId")
	}
	if _, ok := h.srv.session(string(resp.SessionId)); !ok {
		t.Error("session id returned by NewSession is not registered in the server")
	}
}

func TestServer_CloseSessionRemovesFromRegistry(t *testing.T) {
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
	if _, ok := h.srv.session(string(newResp.SessionId)); ok {
		t.Error("session still registered after CloseSession")
	}
}

func TestServer_LogoutIsUnsupported(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// Logout has no ACP-level session to terminate — Authenticate itself
	// is now a real, implemented method (see auth_test.go), so it's no
	// longer this test's example of an unsupported RPC.
	if _, err := h.clientConn.Logout(ctx, coderacp.LogoutRequest{}); err == nil {
		t.Error("expected Logout to fail (not meaningful without ACP-level session state)")
	}
}
