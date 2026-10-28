package acp

import (
	"context"
	"errors"

	"github.com/yottadynamics/yottacode/internal/adapter"

	coderacp "github.com/coder/acp-go-sdk"
)

var errControllerOwned = coderacp.NewInvalidParams(map[string]any{"error": "session already has a controller"})

func (s *acpSession) attach(conn outbound, role string, takeover bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return "", coderacp.NewInvalidParams(map[string]any{"error": "session is closing"})
	}
	if role == "controller" {
		if s.controller != nil && !sameOutbound(s.controller, conn) && !takeover {
			return "", errControllerOwned
		}
		s.controller = conn
		s.conn = s.routing()
		return role, nil
	}
	for _, existing := range s.observers {
		if sameOutbound(existing, conn) {
			return role, nil
		}
	}
	s.observers = append(s.observers, conn)
	return role, nil
}

// canControl reports whether conn currently owns the session controller role.
func (s *acpSession) canControl(conn outbound) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controller == nil || sameOutbound(s.controller, conn)
}

func (s *acpSession) replay(ctx context.Context, conn outbound) (int, error) {
	s.historyMu.Lock()
	messages := append([]adapter.Message(nil), s.rt.Session.Messages...)
	s.historyMu.Unlock()
	count := 0
	for _, msg := range messages {
		var update coderacp.SessionUpdate
		switch msg.Role {
		case adapter.RoleUser:
			update = coderacp.UpdateUserMessageText(msg.Content)
		case adapter.RoleAssistant:
			if msg.Content == "" {
				continue
			}
			update = coderacp.UpdateAgentMessageText(msg.Content)
		default:
			continue
		}
		if err := replayUpdate(ctx, conn, s.id, update); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *acpSession) controlError(conn outbound) error {
	if !s.canControl(conn) {
		return errors.New("session controller is owned by another connection")
	}
	return nil
}
