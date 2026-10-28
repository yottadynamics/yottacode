package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"
)

const maxSessionEvents = 2048

type sessionEvent struct {
	seq    uint64
	update coderacp.SessionUpdate
}

func (s *acpSession) appendEvent(update coderacp.SessionUpdate) uint64 {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.seq++
	s.events = append(s.events, sessionEvent{seq: s.seq, update: update})
	if len(s.events) > maxSessionEvents {
		s.events = s.events[len(s.events)-maxSessionEvents:]
	}
	return s.seq
}

func (s *acpSession) latestSeq() uint64 {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	return s.seq
}

func (s *acpSession) replayEvents(ctx context.Context, conn outbound, after uint64) (int, uint64, error) {
	s.eventMu.Lock()
	events := append([]sessionEvent(nil), s.events...)
	latest := s.seq
	s.eventMu.Unlock()
	count := 0
	for _, event := range events {
		if event.seq <= after {
			continue
		}
		if err := conn.SessionUpdate(ctx, coderacp.SessionNotification{SessionId: coderacp.SessionId(s.id), Update: event.update}); err != nil {
			return count, latest, err
		}
		count++
	}
	return count, latest, nil
}
