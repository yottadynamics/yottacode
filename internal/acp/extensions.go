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
}

type sessionReplayRequest struct {
	SessionID string `json:"sessionId"`
}

type sessionReplayResponse struct {
	SessionID string `json:"sessionId"`
	Messages  int    `json:"messages"`
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
	if req.Cwd != "" {
		if err := a.requireCwd(req.Cwd); err != nil {
			return sessionAttachResponse{}, err
		}
	}
	role, err := sess.attach(a.conn, req.Role, req.Takeover)
	if err != nil {
		return sessionAttachResponse{}, err
	}
	return sessionAttachResponse{SessionID: req.SessionID, Role: role, Owner: role == "controller"}, nil
}

func (a *connectionAgent) replay(ctx context.Context, req sessionReplayRequest) (sessionReplayResponse, error) {
	if err := a.require("read"); err != nil {
		return sessionReplayResponse{}, err
	}
	sess, ok := a.Server.session(req.SessionID)
	if !ok {
		return sessionReplayResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	count, err := sess.replay(ctx, a.conn)
	if err != nil {
		return sessionReplayResponse{}, err
	}
	return sessionReplayResponse{SessionID: req.SessionID, Messages: count}, nil
}

func sameOutbound(a, b outbound) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	return va.Type() == vb.Type() && va.Kind() == reflect.Ptr && va.Pointer() == vb.Pointer()
}

var _ coderacp.ExtensionMethodHandler = (*connectionAgent)(nil)
