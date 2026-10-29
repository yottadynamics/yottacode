package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"
)

// sessionOutbound fans updates to the current controller and observers while
// routing permission requests only to the current controller.
type sessionOutbound struct{ session *acpSession }

func (o sessionOutbound) SessionUpdate(ctx context.Context, n coderacp.SessionNotification) error {
	o.session.appendEvent(n.Update)
	var firstErr error
	for _, target := range o.session.targets() {
		if err := target.SessionUpdate(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (o sessionOutbound) RequestPermission(ctx context.Context, req coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	o.session.mu.Lock()
	allowed := o.session.controllerScopes == nil || o.session.controllerScopes["admin"] || o.session.controllerScopes["approve"]
	o.session.mu.Unlock()
	if !allowed {
		return coderacp.RequestPermissionResponse{}, context.Canceled
	}
	return o.session.requestPermission(ctx, req)
}

func (s *acpSession) routing() outbound { return sessionOutbound{session: s} }

func (s *acpSession) targets() []outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]outbound, 0, 1+len(s.observers))
	if s.controller != nil {
		out = append(out, s.controller)
	}
	out = append(out, s.observers...)
	return out
}
