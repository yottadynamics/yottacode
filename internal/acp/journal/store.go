package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yottadynamics/yottacode/internal/syncutil"
	"os"
	"path/filepath"

	coderacp "github.com/coder/acp-go-sdk"
)

const MaxEvents = 2048

type Event struct {
	Seq    uint64                 `json:"seq"`
	Update coderacp.SessionUpdate `json:"update"`
}

type Store struct {
	path   string
	mu     syncutil.Mutex
	events []Event
	next   uint64
}

func New(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Append(update coderacp.SessionUpdate) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.events = append(s.events, Event{Seq: s.next, Update: update})
	if len(s.events) > MaxEvents {
		s.events = s.events[len(s.events)-MaxEvents:]
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return s.next, nil
}

func (s *Store) Since(after uint64) ([]Event, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > 0 && after+1 < s.events[0].Seq {
		return nil, s.next, true
	}
	out := make([]Event, 0)
	for _, event := range s.events {
		if event.Seq > after {
			out = append(out, event)
		}
	}
	return out, s.next, false
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ACP journal: %w", err)
	}
	var payload struct {
		Next   uint64  `json:"next"`
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return fmt.Errorf("parse ACP journal: %w", err)
	}
	s.next, s.events = payload.Next, payload.Events
	if len(s.events) > MaxEvents {
		s.events = s.events[len(s.events)-MaxEvents:]
	}
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Next   uint64  `json:"next"`
		Events []Event `json:"events"`
	}{s.next, s.events}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".acp-journal-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
