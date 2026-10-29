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
	var seq uint64
	if s.journal != nil {
		if persisted, err := s.journal.Append(update); err == nil {
			seq = persisted
			s.journalHealthy = true
		} else {
			s.seq++
			seq = s.seq
			s.journalHealthy = false
		}
	} else {
		s.seq++
		seq = s.seq
	}
	if seq > s.seq {
		s.seq = seq
	}
	s.events = append(s.events, sessionEvent{seq: seq, update: update})
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
	if s.journal != nil {
		persisted, latest, gap := s.journal.Since(after)
		if gap {
			return 0, latest, coderacp.NewInvalidParams(map[string]any{"error": "replay cursor is too old; snapshot required", "replayGap": true, "snapshotRequired": true})
		}
		count := 0
		for _, event := range persisted {
			if err := conn.SessionUpdate(ctx, coderacp.SessionNotification{SessionId: coderacp.SessionId(s.id), Update: event.Update}); err != nil {
				return count, latest, err
			}
			count++
		}
		return count, latest, nil
	}
	s.eventMu.Lock()
	events := append([]sessionEvent(nil), s.events...)
	latest := s.seq
	oldest := uint64(0)
	if len(events) > 0 {
		oldest = events[0].seq
	}
	s.eventMu.Unlock()
	if oldest > 0 && after+1 < oldest {
		return 0, latest, coderacp.NewInvalidParams(map[string]any{"error": "replay cursor is too old; snapshot required", "replayGap": true, "snapshotRequired": true})
	}
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
