package acp

import (
	"context"
	"errors"
	"strings"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"

	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
	"github.com/yottadynamics/yottacode/internal/cli"
)

// TestInitialize_AdvertisesAllAuthMethods guards the ACP Registry's hard
// requirement (CI-verified, not just a nicety — see
// roadmap/acp-adapter.md's Phase 2 notes): initialize must return a
// non-empty authMethods list with at least one "agent" or "terminal"
// type. Driven over the real wire (not a direct Go call) so the JSON-RPC
// union-type marshaling is exercised too, not just the Go struct
// literal.
func TestInitialize_AdvertisesAllAuthMethods(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(resp.AuthMethods) != 3 {
		t.Fatalf("AuthMethods = %d entries, want 3: %+v", len(resp.AuthMethods), resp.AuthMethods)
	}

	agent := resp.AuthMethods[0]
	if agent.Agent == nil {
		t.Fatalf("AuthMethods[0] is not an Agent-type method: %+v", agent)
	}
	if agent.Agent.Id != authMethodOpenAIChatGPT {
		t.Errorf("AuthMethods[0].Agent.Id = %q, want %q", agent.Agent.Id, authMethodOpenAIChatGPT)
	}

	term := resp.AuthMethods[1]
	if term.Terminal == nil {
		t.Fatalf("AuthMethods[1] is not a Terminal-type method: %+v", term)
	}
	if term.Terminal.Id != authMethodGitHubCopilot {
		t.Errorf("AuthMethods[1].Terminal.Id = %q, want %q", term.Terminal.Id, authMethodGitHubCopilot)
	}
	wantArgs := []string{"copilot-auth", "login"}
	if len(term.Terminal.Args) != len(wantArgs) || term.Terminal.Args[0] != wantArgs[0] || term.Terminal.Args[1] != wantArgs[1] {
		t.Errorf("AuthMethods[1].Terminal.Args = %v, want %v (must point at the existing copilot-auth login subcommand)", term.Terminal.Args, wantArgs)
	}

	vertex := resp.AuthMethods[2]
	if vertex.Agent == nil {
		t.Fatalf("AuthMethods[2] is not an Agent-type method: %+v", vertex)
	}
	if vertex.Agent.Id != authMethodVertexADC {
		t.Errorf("AuthMethods[2].Agent.Id = %q, want %q", vertex.Agent.Id, authMethodVertexADC)
	}
}

// TestAuthenticate_OpenAIChatGPT_Success drives the openai-chatgpt
// method through a fake authenticateOpenAI (no real browser/OAuth round
// trip — that flow is internal/auth/openai's own responsibility to
// test) and confirms a successful login round-trips as an empty,
// error-free AuthenticateResponse.
func TestAuthenticate_OpenAIChatGPT_Success(t *testing.T) {
	h := newTestHarness(t)
	var called bool
	h.srv.authenticateOpenAI = func(context.Context) error {
		called = true
		return nil
	}
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: authMethodOpenAIChatGPT}); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !called {
		t.Error("authenticateOpenAI was never invoked")
	}
}

// TestAuthenticate_OpenAIChatGPT_Failure confirms a failed login
// surfaces as a genuine JSON-RPC error, not a silently-successful
// response — a client must be able to tell login failed.
func TestAuthenticate_OpenAIChatGPT_Failure(t *testing.T) {
	h := newTestHarness(t)
	h.srv.authenticateOpenAI = func(context.Context) error {
		return errors.New("boom")
	}
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: authMethodOpenAIChatGPT})
	if err == nil {
		t.Fatal("expected an error when authenticateOpenAI fails")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should surface the underlying failure; got %v", err)
	}
}

// TestAuthenticate_GitHubCopilot_ExplainsTerminalAuth confirms a client
// that calls authenticate for github-copilot anyway (rather than
// relaunching the binary per Terminal Auth) gets an explanatory error,
// not a silent no-op that leaves it thinking it's authenticated.
func TestAuthenticate_GitHubCopilot_ExplainsTerminalAuth(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: authMethodGitHubCopilot})
	if err == nil {
		t.Fatal("expected an error — github-copilot should never be called via authenticate")
	}
	if !strings.Contains(err.Error(), "Terminal Auth") && !strings.Contains(err.Error(), "copilot-auth login") {
		t.Errorf("error should explain the Terminal Auth relaunch path; got %v", err)
	}
}

// TestAuthenticate_VertexADC_Success drives the vertex-adc method
// through a fake authenticateVertex (no real gcloud/ADC round trip —
// that's internal/auth/vertex's own responsibility to test) and
// confirms success round-trips as an empty, error-free
// AuthenticateResponse, same contract as openai-chatgpt.
func TestAuthenticate_VertexADC_Success(t *testing.T) {
	h := newTestHarness(t)
	var called bool
	h.srv.authenticateVertex = func(context.Context) error {
		called = true
		return nil
	}
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: authMethodVertexADC}); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !called {
		t.Error("authenticateVertex was never invoked")
	}
}

// TestAuthenticate_VertexADC_Failure confirms a failed ADC check (e.g.
// no `gcloud auth application-default login` ever run) surfaces as a
// genuine JSON-RPC error, matching openai-chatgpt's contract.
func TestAuthenticate_VertexADC_Failure(t *testing.T) {
	h := newTestHarness(t)
	h.srv.authenticateVertex = func(context.Context) error {
		return errors.New("no Application Default Credentials found")
	}
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: authMethodVertexADC})
	if err == nil {
		t.Fatal("expected an error when authenticateVertex fails")
	}
	if !strings.Contains(err.Error(), "Application Default Credentials") {
		t.Errorf("error should surface the underlying failure; got %v", err)
	}
}

// TestAuthenticateVertex_RejectsWrongProviderKind exercises the real
// (non-faked) authenticateVertex function's guard clause: a
// `yottacode acp` process launched for a non-Vertex provider (the
// common case — most sessions use --provider-kind openai/anthropic/
// etc.) must fail clearly rather than attempting a scan against a
// base_url that isn't a Vertex endpoint at all.
func TestAuthenticateVertex_RejectsWrongProviderKind(t *testing.T) {
	err := authenticateVertex(context.Background(), cli.ChatOptions{
		ProviderKind: "openai",
		BaseURL:      "https://api.openai.com/v1",
	})
	if err == nil {
		t.Fatal("expected an error for a non-Vertex provider kind")
	}
	if !strings.Contains(err.Error(), "not configured for a Vertex provider") {
		t.Errorf("error should explain the provider-kind mismatch; got %v", err)
	}
}

// TestAuthenticateVertex_RejectsEmptyBaseURL exercises the second guard
// clause: Vertex's base_url encodes the GCP project/location, so a
// missing one can't be scanned regardless of provider kind.
func TestAuthenticateVertex_RejectsEmptyBaseURL(t *testing.T) {
	err := authenticateVertex(context.Background(), cli.ChatOptions{
		ProviderKind: vertexauth.ProviderVertex,
		BaseURL:      "",
	})
	if err == nil {
		t.Fatal("expected an error for an empty base_url")
	}
	if !strings.Contains(err.Error(), "base-url") {
		t.Errorf("error should mention the missing base_url; got %v", err)
	}
}

func TestAuthenticate_UnknownMethod(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := h.clientConn.Authenticate(ctx, coderacp.AuthenticateRequest{MethodId: "does-not-exist"})
	if err == nil {
		t.Fatal("expected an error for an unknown auth method id")
	}
}
