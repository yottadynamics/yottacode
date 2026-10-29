package acp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	coderacp "github.com/coder/acp-go-sdk"
)

const (
	extAttach   = "_yottacode/session/attach"
	extReplay   = "_yottacode/session/replay"
	extTakeover = "_yottacode/session/takeover"
)

type sessionAttachRequest struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
	Role      string `json:"role,omitempty"`
	Takeover  bool   `json:"takeover,omitempty"`
}

type sessionAttachResponse struct {
	SessionID string `json:"sessionId"`
	Role      string `json:"role"`
	Owner     bool   `json:"owner"`
	LatestSeq uint64 `json:"latestSeq"`
}

type sessionReplayRequest struct {
	SessionID string `json:"sessionId"`
	AfterSeq  uint64 `json:"afterSeq,omitempty"`
}

type sessionReplayResponse struct {
	SessionID        string `json:"sessionId"`
	Messages         int    `json:"messages"`
	LatestSeq        uint64 `json:"latestSeq"`
	ReplayGap        bool   `json:"replayGap,omitempty"`
	SnapshotRequired bool   `json:"snapshotRequired,omitempty"`
}

func (a *connectionAgent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case extAttach:
		var req sessionAttachRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		return a.attach(ctx, req)
	case extReplay:
		var req sessionReplayRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		return a.replay(ctx, req)
	case extTakeover:
		var req sessionAttachRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		req.Takeover = true
		return a.attach(ctx, req)
	default:
		return nil, coderacp.NewMethodNotFound(method)
	}
}

func (a *connectionAgent) attach(ctx context.Context, req sessionAttachRequest) (sessionAttachResponse, error) {
	if err := a.require("read"); err != nil {
		return sessionAttachResponse{}, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return sessionAttachResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "sessionId is required"})
	}
	if req.Role == "" {
		req.Role = "observer"
	}
	if req.Role != "observer" && req.Role != "controller" {
		return sessionAttachResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "role must be observer or controller"})
	}
	if req.Role == "controller" || req.Takeover {
		if err := a.require("prompt"); err != nil {
			return sessionAttachResponse{}, err
		}
	}
	sess, ok := a.Server.session(req.SessionID)
	if !ok {
		return sessionAttachResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	if !sessAllowsProject(sess, a.rootCheck) {
		return sessionAttachResponse{}, coderacp.NewAuthRequired(map[string]any{"error": "token is not authorized for this project"})
	}
	if req.Cwd != "" {
		if err := a.requireCwd(req.Cwd); err != nil {
			return sessionAttachResponse{}, err
		}
	}
	role, err := sess.attach(a.conn, a.scopes, req.Role, req.Takeover)
	if err != nil {
		return sessionAttachResponse{}, err
	}
	return sessionAttachResponse{SessionID: req.SessionID, Role: role, Owner: role == "controller", LatestSeq: sess.latestSeq()}, nil
}

func sessAllowsProject(sess *acpSession, check func(string) bool) bool {
	if check == nil {
		return true
	}
	sess.historyMu.Lock()
	cwd := sess.rt.Session.Cwd
	sess.historyMu.Unlock()
	return check(cwd)
}

func (a *connectionAgent) replay(ctx context.Context, req sessionReplayRequest) (sessionReplayResponse, error) {
	if err := a.require("read"); err != nil {
		return sessionReplayResponse{}, err
	}
	sess, ok := a.Server.session(req.SessionID)
	if !ok {
		return sessionReplayResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	if !sessAllowsProject(sess, a.rootCheck) {
		return sessionReplayResponse{}, coderacp.NewAuthRequired(map[string]any{"error": "token is not authorized for this project"})
	}
	count, latest, err := sess.replay(ctx, a.conn, req.AfterSeq)
	if err != nil {
		return sessionReplayResponse{}, err
	}
	return sessionReplayResponse{SessionID: req.SessionID, Messages: count, LatestSeq: latest}, nil
}

func sameOutbound(a, b outbound) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	return va.Type() == vb.Type() && va.Kind() == reflect.Ptr && va.Pointer() == vb.Pointer()
}

var _ coderacp.ExtensionMethodHandler = (*connectionAgent)(nil)
