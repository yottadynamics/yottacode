package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"
)

// sessionOutbound fans updates to the current controller and observers while
// routing permission requests only to the current controller.
type sessionOutbound struct{ session *acpSession }

func (o sessionOutbound) SessionUpdate(ctx context.Context, n coderacp.SessionNotification) error {
	var firstErr error
	for _, target := range o.session.targets() {
		if err := target.SessionUpdate(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (o sessionOutbound) RequestPermission(ctx context.Context, req coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	target := o.session.controllerTarget()
	if target == nil {
		return coderacp.RequestPermissionResponse{}, context.Canceled
	}
	return target.RequestPermission(ctx, req)
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

func (s *acpSession) controllerTarget() outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controller
}
