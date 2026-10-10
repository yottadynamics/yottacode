// Package acp implements yottacode as an Agent Client Protocol (ACP)
// server — a long-lived process hosting N concurrent sessions over one
// stdio JSON-RPC connection, so yottacode is embeddable in editors that
// speak ACP (Zed, JetBrains, and others). See roadmap/acp-adapter.md for
// the full design.
//
// Session construction reuses internal/agentruntime.Builder, the same
// world-building path internal/oneshot and internal/tui use — this
// package is protocol plumbing and event/permission translation on top
// of that shared core, not a fourth copy of it.
package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// Server implements coderacp.Agent (and coderacp.AgentLoader) on behalf
// of yottacode. One Server hosts every session for the process's
// lifetime; the reader/writer/framing discipline that makes that safe
// over a single stdio pair is coderacp.AgentSideConnection's job, not
// this package's — see roadmap/acp-adapter.md's Concurrency section.
type Server struct {
	conn *coderacp.AgentSideConnection

	// BaseOpts carries process-level defaults (model, provider, API key,
	// reasoning effort, search flags, etc.) resolved from flags/env
	// before the acp server starts. ACP's session/new has no equivalent
	// of yottacode's CLI flags — the client only ever supplies cwd and
	// mcpServers — so every session's SessionSpec is seeded from this.
	BaseOpts cli.ChatOptions

	// authenticateOpenAI drives the "openai-chatgpt" Agent Auth method
	// (see Authenticate, auth.go). A field rather than a hardcoded call
	// so tests can inject a fake instead of running a real browser/OAuth
	// round-trip — defaulted in NewServer.
	authenticateOpenAI func(ctx context.Context) error

	// authenticateVertex drives the "vertex-adc" Agent Auth method (see
	// Authenticate, auth.go). Same test-injectability rationale as
	// authenticateOpenAI — defaulted in NewServer to a closure reading
	// BaseOpts, since unlike authenticateOpenAIChatGPT the Vertex check
	// needs this session's configured provider kind and base URL.
	authenticateVertex func(ctx context.Context) error

	mu       syncutil.RWMutex
	sessions map[string]*acpSession
}

var (
	_ coderacp.Agent       = (*Server)(nil)
	_ coderacp.AgentLoader = (*Server)(nil)
)

// NewServer constructs a Server. BaseOpts should already be fully
// resolved (cli.Resolve applied) by the caller, mirroring how
// cmd/yottacode/main.go resolves opts before calling oneshot.Run/tui.Run.
func NewServer(baseOpts cli.ChatOptions) *Server {
	s := &Server{
		BaseOpts:           baseOpts,
		sessions:           make(map[string]*acpSession),
		authenticateOpenAI: authenticateOpenAIChatGPT,
	}
	s.authenticateVertex = func(ctx context.Context) error {
		return authenticateVertex(ctx, s.BaseOpts)
	}
	return s
}

// SetConnection wires the connection the Server uses for outbound calls
// (session/update, session/request_permission). Must be called before
// any inbound request can be serviced — cmd/yottacode/acp.go does this
// immediately after constructing the connection, mirroring
// coder/acp-go-sdk's own example/agent pattern (SetAgentConnection).
func (s *Server) SetConnection(conn *coderacp.AgentSideConnection) {
	s.conn = conn
}

// Initialize negotiates protocol version and capabilities. Exactly once,
// before anything else, per the ACP spec.
//
// AuthMethods matters beyond protocol correctness: the ACP Registry
// requires a non-empty list here (CI-verified — see
// roadmap/acp-adapter.md's Phase 2 notes) with at least one method of
// type "agent" or "terminal". See auth.go for both implementations.
func (s *Server) Initialize(_ context.Context, req coderacp.InitializeRequest) (coderacp.InitializeResponse, error) {
	// Version negotiation per the ACP spec: an unsupported requested
	// version is not an error — reply with the latest version we support
	// and let the client decide whether to proceed. Rejecting here broke
	// clients (e.g. buzz-acp) that send a different version number.
	return coderacp.InitializeResponse{
		ProtocolVersion: coderacp.ProtocolVersionNumber,
		AgentCapabilities: coderacp.AgentCapabilities{
			// session/load ↔ session.Load interop (roadmap §Storage).
			LoadSession: true,
			// A spec-compliant client only sends Http/Sse McpServer
			// entries once this is advertised — see convertMCPServers.
			McpCapabilities: coderacp.McpCapabilities{Http: true, Sse: true},
		},
		AuthMethods: authMethods(),
	}, nil
}

// Logout is not meaningful without ACP-level authentication.
func (s *Server) Logout(_ context.Context, _ coderacp.LogoutRequest) (coderacp.LogoutResponse, error) {
	return coderacp.LogoutResponse{}, coderacp.NewMethodNotFound("logout")
}

// ListSessions is not advertised (no sessionCapabilities.list) and not
// implemented in v1.
func (s *Server) ListSessions(_ context.Context, _ coderacp.ListSessionsRequest) (coderacp.ListSessionsResponse, error) {
	return coderacp.ListSessionsResponse{}, coderacp.NewMethodNotFound("session/list")
}

// ResumeSession is not advertised (no sessionCapabilities.resume) — v1
// supports the same resume path via LoadSession instead.
func (s *Server) ResumeSession(_ context.Context, _ coderacp.ResumeSessionRequest) (coderacp.ResumeSessionResponse, error) {
	return coderacp.ResumeSessionResponse{}, coderacp.NewMethodNotFound("session/resume")
}

// SetSessionConfigOption is implemented in config_options.go — reasoning
// effort and, when a router pair is configured, advisor/implementer
// routing.

// NewSession creates a session via agentruntime.Builder and registers it.
func (s *Server) NewSession(ctx context.Context, params coderacp.NewSessionRequest) (coderacp.NewSessionResponse, error) {
	if err := validateMCPServers(params.McpServers); err != nil {
		return coderacp.NewSessionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	spec := s.sessionSpec(params.Cwd, params.McpServers)
	rt, err := agentruntime.NewBuilder().Build(ctx, spec)
	if err != nil {
		return coderacp.NewSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": err.Error()})
	}
	// Hosts such as Buzz pass a session title out-of-band in _meta
	// (--session-title); use it as the session's name so it is
	// recognisable in /load. Persisted with the session's next save.
	if title, _ := params.Meta["sessionTitle"].(string); strings.TrimSpace(title) != "" {
		// Suffix the creation time (UTC, like the session id) so sessions
		// from the same agent stay distinguishable and resumable by name.
		rt.Session.Name = sessionTitleName(strings.TrimSpace(title), rt.Session.Created)
	}
	sess := newACPSession(s, rt)
	s.mu.Lock()
	s.sessions[rt.Session.ID] = sess
	s.mu.Unlock()
	if err := s.sendAvailableCommands(ctx, rt.Session.ID); err != nil {
		sess.requestCancel()
		sess.waitForTurn(ctx, closeSessionDrainTimeout)
		sess.rt.Close(context.Background())
		s.mu.Lock()
		delete(s.sessions, rt.Session.ID)
		s.mu.Unlock()
		return coderacp.NewSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "send available commands: " + err.Error()})
	}
	if s.conn != nil {
		for _, warning := range rt.Warnings {
			if err := s.replayUpdate(ctx, rt.Session.ID, coderacp.UpdateAgentMessageText("[warning] "+truncateOneLine(warning, 240)+"\n")); err != nil {
				rt.Close(context.Background())
				s.mu.Lock()
				delete(s.sessions, rt.Session.ID)
				s.mu.Unlock()
				return coderacp.NewSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "send warning: " + err.Error()})
			}
		}
	}
	return coderacp.NewSessionResponse{
		SessionId:     coderacp.SessionId(rt.Session.ID),
		Modes:         sessionModeState(rt),
		ConfigOptions: sessionConfigOptions(rt),
	}, nil
}

// LoadSession resumes a persisted session by id — session/load becomes
// session.Load via SessionSpec.Resume, same as the TUI's --continue.
func (s *Server) LoadSession(ctx context.Context, params coderacp.LoadSessionRequest) (coderacp.LoadSessionResponse, error) {
	if err := validateMCPServers(params.McpServers); err != nil {
		return coderacp.LoadSessionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	spec := s.sessionSpec(params.Cwd, params.McpServers)
	spec.Resume = string(params.SessionId)
	rt, err := agentruntime.NewBuilder().Build(ctx, spec)
	if err != nil {
		return coderacp.LoadSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": err.Error()})
	}
	// The adapter is built from process defaults; restore the model this
	// session last ran on. Best effort: a model no longer in config simply
	// keeps the default rather than failing the load.
	if saved := rt.Session.Model; saved != "" && (saved != rt.ChatOptions.Model || rt.Session.Provider != "") {
		if rt.Session.Provider != "" {
			rt.ChatOptions.Provider = rt.Session.Provider
		}
		_ = agentruntime.RebuildAdapterForModel(rt, saved)
	}
	sess := newACPSession(s, rt)

	s.mu.Lock()
	old, hadOld := s.sessions[rt.Session.ID]
	s.mu.Unlock()
	if hadOld {
		// Block new prompts before waiting for the current lifecycle owner.
		old.mu.Lock()
		old.closing = true
		old.mu.Unlock()
		old.lifecycle.Lock()
		old.requestCancel()
		drained := old.waitForTurn(ctx, closeSessionDrainTimeout)
		if !drained {
			old.mu.Lock()
			old.closing = false
			old.mu.Unlock()
			old.lifecycle.Unlock()
			rt.Close(context.Background())
			return coderacp.LoadSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "timed out draining replaced session"})
		}
		old.mu.Lock()
		old.closed = true
		old.mu.Unlock()
		old.rt.Close(ctx)
		old.lifecycle.Unlock()
	}
	s.mu.Lock()
	s.sessions[rt.Session.ID] = sess
	s.mu.Unlock()

	if err := s.sendAvailableCommands(ctx, rt.Session.ID); err != nil {
		rt.Close(context.Background())
		s.mu.Lock()
		delete(s.sessions, rt.Session.ID)
		s.mu.Unlock()
		return coderacp.LoadSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "send available commands: " + err.Error()})
	}
	// Replay persisted conversational transcript as standard ACP updates. Tool
	// and system messages are intentionally omitted: loading restores context,
	// it must not re-run side effects or expose internal instructions.
	for _, msg := range rt.Session.Messages {
		switch msg.Role {
		case adapter.RoleUser:
			if err := s.replayUpdate(ctx, rt.Session.ID, coderacp.UpdateUserMessageText(msg.Content)); err != nil {
				rt.Close(context.Background())
				s.mu.Lock()
				delete(s.sessions, rt.Session.ID)
				s.mu.Unlock()
				return coderacp.LoadSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "replay session: " + err.Error()})
			}
		case adapter.RoleAssistant:
			if msg.Content != "" {
				if err := s.replayUpdate(ctx, rt.Session.ID, coderacp.UpdateAgentMessageText(msg.Content)); err != nil {
					rt.Close(context.Background())
					s.mu.Lock()
					delete(s.sessions, rt.Session.ID)
					s.mu.Unlock()
					return coderacp.LoadSessionResponse{}, coderacp.NewInternalError(map[string]any{"error": "replay session: " + err.Error()})
				}
			}
		}
	}
	// Transcript replay is intentionally limited to standard user and agent
	// message chunks; internal system prompts, historical tool calls, and
	// approvals are restored only in server-side session state.
	return coderacp.LoadSessionResponse{Modes: sessionModeState(rt), ConfigOptions: sessionConfigOptions(rt)}, nil
}

// sessionSpec builds the agentruntime.SessionSpec shared by NewSession
// and LoadSession: BaseOpts seeds everything ACP's session/new has no
// equivalent for, cwd and mcpServers come from the request.
func (s *Server) sessionSpec(cwd string, mcpServers []coderacp.McpServer) agentruntime.SessionSpec {
	return agentruntime.SessionSpec{
		ChatOptions: s.BaseOpts,
		Cwd:         cwd,
		MCPServers:  convertMCPServers(mcpServers),
		// enter_worktree/exit_worktree's process-global os.Chdir is
		// unsafe when one process hosts N concurrent sessions with
		// different cwds — see roadmap/acp-adapter.md's Concurrency
		// section. Excluded for every ACP session, not configurable.
		DisableWorktreeTools: true,
		// ACP is long-lived, same as the TUI: background dispatch is
		// available.
		SupportsBackgroundDispatch: true,
		// No progressive-UI concept in ACP — session/new must return
		// only once the session is actually ready, so MCP starts
		// synchronously inside Build.
		DeferMCPStart: false,
		// The "Advisor routing" session config option (config_options.go)
		// can flip routing live mid-session — the pair must be
		// pre-resolved at startup for that to work.
		SupportsLiveRouterToggle: true,
	}
}

func validateMCPServers(servers []coderacp.McpServer) error {
	for i, s := range servers {
		variants := 0
		if s.Stdio != nil {
			variants++
		}
		if s.Http != nil {
			variants++
		}
		if s.Sse != nil {
			variants++
		}
		if variants != 1 {
			return fmt.Errorf("acp: mcp server %d must specify exactly one supported variant", i)
		}
		if s.Stdio != nil && (s.Stdio.Name == "" || s.Stdio.Command == "") {
			return fmt.Errorf("acp: mcp server %d has empty stdio name or command", i)
		}
		if s.Http != nil && (s.Http.Name == "" || s.Http.Url == "") {
			return fmt.Errorf("acp: mcp server %d has empty http name or url", i)
		}
		if s.Sse != nil && (s.Sse.Name == "" || s.Sse.Url == "") {
			return fmt.Errorf("acp: mcp server %d has empty sse name or url", i)
		}
	}
	return nil
}

// convertMCPServers translates ACP's supported MCP variants.
func convertMCPServers(servers []coderacp.McpServer) []config.MCPServer {
	if len(servers) == 0 {
		return nil
	}
	out := make([]config.MCPServer, 0, len(servers))
	for _, s := range servers {
		switch {
		case s.Stdio != nil:
			env := make(map[string]string, len(s.Stdio.Env))
			for _, e := range s.Stdio.Env {
				env[e.Name] = e.Value
			}
			out = append(out, config.MCPServer{
				Name:    s.Stdio.Name,
				Command: s.Stdio.Command,
				Args:    s.Stdio.Args,
				Env:     env,
			})
		case s.Http != nil:
			out = append(out, config.MCPServer{
				Name:      s.Http.Name,
				Transport: "http",
				URL:       s.Http.Url,
				Headers:   httpHeadersToMap(s.Http.Headers),
			})
		case s.Sse != nil:
			out = append(out, config.MCPServer{
				Name:      s.Sse.Name,
				Transport: "sse",
				URL:       s.Sse.Url,
				Headers:   httpHeadersToMap(s.Sse.Headers),
			})
		}
	}
	return out
}

func httpHeadersToMap(headers []coderacp.HttpHeader) map[string]string {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		out[h.Name] = h.Value
	}
	return out
}

// replayUpdate sends one load transcript update and keeps outbound failures
// visible to the caller.
func (s *Server) replayUpdate(ctx context.Context, id string, update coderacp.SessionUpdate) error {
	if s.conn == nil {
		return nil
	}
	return s.conn.SessionUpdate(ctx, coderacp.SessionNotification{SessionId: coderacp.SessionId(id), Update: update})
}

// session looks up a registered session by id under the read lock.
func (s *Server) session(id string) (*acpSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// sessionTitleName builds "<title>-YYYYMMDD-HHMMSS" from a host-supplied
// title and the session's creation time. No spaces, so it is typeable as
// `--resume <name>`.
func sessionTitleName(title string, created time.Time) string {
	if created.IsZero() {
		return title
	}
	return title + "-" + created.UTC().Format("20060102-150405")
}
