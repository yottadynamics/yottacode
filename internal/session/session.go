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
	"github.com/yottadynamics/yottacode/internal/agent"
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
	// Todos is the working plan written by the todo_write tool. Omitted
	// from JSON when empty so older session files load unchanged.
	Todos []agent.Todo `json:"todos,omitempty"`
	// Worktree is the yottacode-managed worktree name this session was
	// launched in (via `yottacode --worktree <name>`). Empty for sessions
	// running against the main checkout. Stored so `sessions resume`
	// lands back in the correct worktree dir even if the user moved or
	// renamed the repo. Omitted from JSON when empty so existing session
	// files load unchanged.
	Worktree string `json:"worktree,omitempty"`
	// TotalUsage is the cumulative token tally across every assistant
	// turn in this session. Written by AddUsage on each EventDone.
	// Omitted from JSON when zero so existing session files load
	// byte-identical until the first usage is recorded.
	TotalUsage adapter.Usage `json:"total_usage,omitzero"`
	// ModelUsage is per-model breakdown — users mix models within a
	// session (Anthropic for code review, Gemini for grep, etc.) and
	// the cost calculator needs to know which model produced each
	// turn. Keyed by model ID exactly as the adapter reported it.
	ModelUsage map[string]adapter.Usage `json:"model_usage,omitempty"`

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

// LatestInCwd returns the most recent saved session whose Cwd matches
// the given directory. Used by `yottacode --continue` (mirroring
// Claude Code's --continue) to skip the picker and resume the
// directory's last session directly. Returns an error wrapping
// errNoSessionInCwd when no saved session matches.
//
// "Most recent" is determined by sorting all matches descending by
// Session.Created, falling back to the timestamp-prefixed ID when two
// sessions share an identical Created (test fixtures). The Cwd
// comparison is exact-string match — symlinked or differently-resolved
// paths won't unify; users hit by that should pass the matching path
// explicitly.
func LatestInCwd(cwd string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var matches []Session
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
		if s.Cwd != cwd {
			continue
		}
		s.path = p
		matches = append(matches, s)
	}
	if len(matches) == 0 {
		return nil, ErrNoSessionInCwd
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Created.Equal(matches[j].Created) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].Created.After(matches[j].Created)
	})
	out := matches[0]
	return &out, nil
}

// ErrNoSessionInCwd is returned by LatestInCwd when no saved session
// has a Cwd field matching the requested directory. Sentinel so
// callers can present a friendly "no prior session in this directory"
// error without string matching.
var ErrNoSessionInCwd = errors.New("no saved session in this directory")

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
			Worktree: s.Worktree,
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
	// Worktree is the yottacode worktree name this session ran in, or
	// empty for the main checkout. Surfaced in `yottacode sessions list`
	// output so users can tell which sessions belong to which worktree.
	Worktree string
}

// AddUsage records the per-turn usage that just landed on an
// assistant message into the session's running totals. Safe to call
// with a nil receiver or nil usage — both branches no-op. Caller is
// responsible for Save() if the new totals should be persisted now;
// most callers persist on the same cadence as Messages.
func (s *Session) AddUsage(model string, u *adapter.Usage) {
	if s == nil || u == nil {
		return
	}
	s.TotalUsage.Add(u)
	if model == "" {
		return
	}
	if s.ModelUsage == nil {
		s.ModelUsage = map[string]adapter.Usage{}
	}
	prev := s.ModelUsage[model]
	prev.Add(u)
	s.ModelUsage[model] = prev
}

// SessionUsageSummary is a stripped per-session view used by the
// daily-rollup scan. We avoid decoding Messages (the heavy field) so
// /usage can scan dozens of session files cheaply.
type SessionUsageSummary struct {
	ID         string
	Name       string
	Model      string
	Created    time.Time
	TotalUsage adapter.Usage
	ModelUsage map[string]adapter.Usage
}

// UsageSince scans every saved session newer than t and returns a
// per-session usage summary. Decodes only the lightweight metadata +
// usage fields — Messages stay on disk, keeping the scan cheap.
// Sessions older than t are filtered out by Created; sessions with
// no usage data still appear so the daily rollup can show "N
// sessions, no token data yet."
func UsageSince(t time.Time) ([]SessionUsageSummary, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionUsageSummary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var stub struct {
			ID         string                   `json:"id"`
			Name       string                   `json:"name"`
			Model      string                   `json:"model"`
			Created    time.Time                `json:"created"`
			TotalUsage adapter.Usage            `json:"total_usage"`
			ModelUsage map[string]adapter.Usage `json:"model_usage"`
		}
		if err := json.Unmarshal(b, &stub); err != nil {
			continue
		}
		if stub.Created.Before(t) {
			continue
		}
		out = append(out, SessionUsageSummary{
			ID:         stub.ID,
			Name:       stub.Name,
			Model:      stub.Model,
			Created:    stub.Created,
			TotalUsage: stub.TotalUsage,
			ModelUsage: stub.ModelUsage,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Save atomically writes the session to disk.
//
// The temp file gets a unique name (os.CreateTemp) rather than a fixed
// "<path>.tmp" suffix: two yottacode processes editing the same session
// (e.g. two terminals, or one `--continue` alongside a `--resume`) would
// otherwise write to the same temp path and clobber each other's
// in-flight write, so one process's history would be silently lost on
// rename. A per-write unique temp name makes concurrent saves
// last-writer-wins on the final file instead of corrupting it.
func (s *Session) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds; a
	// successful rename consumes tmpName so the Remove is a harmless no-op.
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	// fsync the data before rename, and the directory after, so a
	// crash/power-loss just after Save returns can't leave the
	// transcript (the highest-value durable artifact) zero-length or
	// stale — matching the memory layer's atomic writes.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	// Best-effort directory fsync so the rename itself is durable.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

