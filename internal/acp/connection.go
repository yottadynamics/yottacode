package acp

import (
	"context"
	"sync"

	coderacp "github.com/coder/acp-go-sdk"
)

// outbound is the small portion of an ACP client connection needed by a
// running session. Keeping it application-owned lets one Server share sessions
// across many transport connections without coupling session state to one SDK
// connection instance.
type outbound interface {
	SessionUpdate(context.Context, coderacp.SessionNotification) error
	RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error)
}

// ConnectionRef breaks the SDK's construction cycle: the inbound agent needs
// an outbound connection, while NewAgentSideConnection needs the inbound
// agent. It is set immediately after the SDK connection is constructed.
type ConnectionRef struct {
	mu   sync.RWMutex
	conn outbound
}

// NewConnectionRef creates an empty outbound connection reference.
func NewConnectionRef() *ConnectionRef { return &ConnectionRef{} }

// Set binds the SDK connection to the reference.
func (r *ConnectionRef) Set(conn *coderacp.AgentSideConnection) {
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
}

func (r *ConnectionRef) current() outbound {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.conn
}

func (r *ConnectionRef) SessionUpdate(ctx context.Context, n coderacp.SessionNotification) error {
	conn := r.current()
	if conn == nil {
		return context.Canceled
	}
	return conn.SessionUpdate(ctx, n)
}

func (r *ConnectionRef) RequestPermission(ctx context.Context, req coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	conn := r.current()
	if conn == nil {
		return coderacp.RequestPermissionResponse{}, context.Canceled
	}
	return conn.RequestPermission(ctx, req)
}

// connectionAgent binds inbound ACP requests to the outbound connection that
// received them. New/load requests record that connection on the session;
// ordinary methods remain shared Server behavior.
type connectionAgent struct {
	*Server
	conn   outbound
	scopes map[string]bool
}

func (a *connectionAgent) require(scope string) error {
	if len(a.scopes) == 0 || a.scopes["admin"] || a.scopes[scope] {
		return nil
	}
	return coderacp.NewAuthRequired(map[string]any{"scope": scope})
}

var _ coderacp.Agent = (*connectionAgent)(nil)
var _ coderacp.AgentLoader = (*connectionAgent)(nil)

func (a *connectionAgent) NewSession(ctx context.Context, params coderacp.NewSessionRequest) (coderacp.NewSessionResponse, error) {
	if err := a.require("prompt"); err != nil {
		return coderacp.NewSessionResponse{}, err
	}
	return a.Server.newSession(ctx, params, a.conn)
}

func (a *connectionAgent) LoadSession(ctx context.Context, params coderacp.LoadSessionRequest) (coderacp.LoadSessionResponse, error) {
	if err := a.require("read"); err != nil {
		return coderacp.LoadSessionResponse{}, err
	}
	return a.Server.loadSession(ctx, params, a.conn)
}

func (a *connectionAgent) Prompt(ctx context.Context, params coderacp.PromptRequest) (coderacp.PromptResponse, error) {
	if err := a.require("prompt"); err != nil {
		return coderacp.PromptResponse{}, err
	}
	return a.Server.Prompt(ctx, params)
}

func (a *connectionAgent) Cancel(ctx context.Context, params coderacp.CancelNotification) error {
	if err := a.require("prompt"); err != nil {
		return err
	}
	return a.Server.Cancel(ctx, params)
}

func (a *connectionAgent) CloseSession(ctx context.Context, params coderacp.CloseSessionRequest) (coderacp.CloseSessionResponse, error) {
	if err := a.require("prompt"); err != nil {
		return coderacp.CloseSessionResponse{}, err
	}
	return a.Server.CloseSession(ctx, params)
}

func (a *connectionAgent) SetSessionConfigOption(ctx context.Context, params coderacp.SetSessionConfigOptionRequest) (coderacp.SetSessionConfigOptionResponse, error) {
	if err := a.require("prompt"); err != nil {
		return coderacp.SetSessionConfigOptionResponse{}, err
	}
	return a.Server.SetSessionConfigOption(ctx, params)
}

func (a *connectionAgent) SetSessionMode(ctx context.Context, params coderacp.SetSessionModeRequest) (coderacp.SetSessionModeResponse, error) {
	if err := a.require("prompt"); err != nil {
		return coderacp.SetSessionModeResponse{}, err
	}
	return a.Server.SetSessionMode(ctx, params)
}

// Agent returns an ACP handler bound to one local/stdio connection. Local
// connections are trusted and retain the existing unrestricted behavior.
func (s *Server) Agent(conn outbound) coderacp.Agent {
	return &connectionAgent{Server: s, conn: conn}
}

// AgentWithScopes returns an ACP handler for an authenticated remote client.
// The scope set is enforced before session-mutating operations are dispatched.
func (s *Server) AgentWithScopes(conn outbound, scopes []string) coderacp.Agent {
	allowed := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		allowed[scope] = true
	}
	return &connectionAgent{Server: s, conn: conn, scopes: allowed}
}
