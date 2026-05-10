package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// Session is one resumable conversation persisted as JSON in
// ~/.yottacode/sessions/<id>.json.
//
// Name is an optional human-readable label set via the /sessions
// picker's Rename action. It's a soft alias: Load will fall back to a
// Name match if the requested id doesn't resolve to a file.
type Session struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Model    string            `json:"model"`
	Created  time.Time         `json:"created"`
	Cwd      string            `json:"cwd"`
	Messages []adapter.Message `json:"messages"`

	path string // filled by New/Load, not serialized
}

func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".yottacode", "sessions")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// New starts a fresh session and reserves its file path.
func New(model, cwd string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405.000000")
	return &Session{
		ID:      id,
		Model:   model,
		Created: time.Now().UTC(),
		Cwd:     cwd,
		path:    filepath.Join(dir, id+".json"),
	}, nil
}

// Load reads a stored session by id (filename match) or name (Name
// field match). The legacy "last" keyword shortcut was retired
// alongside the /resume slash command — the /sessions picker (and the
// `yottacode sessions resume <id|name>` cobra subcommand) is the canonical
// path for "load the most recent" now, with the picker defaulting
// the cursor to the newest entry.
func Load(id string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	if s, err := loadByID(dir, id); err == nil {
		return s, nil
	}
	if s, err := loadByName(dir, id); err == nil {
		return s, nil
	}
	return nil, fmt.Errorf("session load: no session with id or name %q", id)
}

func loadByID(dir, id string) (*Session, error) {
	p := filepath.Join(dir, id+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("session parse %s: %w", p, err)
	}
	s.path = p
	return &s, nil
}

func loadByName(dir, name string) (*Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.Name == name {
			s.path = p
			return &s, nil
		}
	}
	return nil, errors.New("not found by name")
}

// List returns every saved session's metadata, newest first. Doesn't load the
// full message log to keep this cheap for the /sessions slash command.
func List() ([]SessionInfo, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		out = append(out, SessionInfo{
			ID:       s.ID,
			Name:     s.Name,
			Model:    s.Model,
			Created:  s.Created,
			Messages: len(s.Messages),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// SessionInfo is a metadata-only view returned by List.
type SessionInfo struct {
	ID       string
	Name     string
	Model    string
	Created  time.Time
	Messages int
}

// Save atomically writes the session to disk.
func (s *Session) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

