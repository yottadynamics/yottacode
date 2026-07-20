// Sensitive projects — the per-repository opt-out that stops automatic
// session recall from moving a project's conversations across the network.
// See yottacode-roadmap/memory-auto-recall.md (fork 3).
//
// The threat this closes is specific. Embedding, storage, and cosine are all
// local, but an *injected* recall excerpt egresses to the cloud LLM with the
// turn. For a PHI/medical repository that is unacceptable, and it cuts both
// ways:
//
//   - inbound — auto-recall is off inside a sensitive project, so nothing is
//     swept into its prompts automatically;
//   - outbound — a sensitive project's sessions are never candidates for any
//     *other* project's recall, whatever the configured scope. Without this
//     half, `scope = "user"` would quietly carry PHI into an unrelated repo's
//     turn, which is the same leak facing the other direction.
//
// What it deliberately does NOT do: sessions are still indexed and embedded,
// and the manual session_recall tool still reaches them. The gate is about
// what leaves *automatically*, not about making your own history unreachable
// when you deliberately ask for it. Use Full quarantine semantics elsewhere if
// that is ever needed.
//
// Storage mirrors internal/trust deliberately — same JSON shape, same atomic
// write, same descendant inheritance, same DefaultDenyPaths entry so the
// model's write tools cannot mark a repo non-sensitive. Two small stores with
// one obvious pattern beat one generic store with a mode flag.
package sensitive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is the on-disk schema version. Bumped only on a breaking change;
// readers tolerate unknown fields so additive changes don't require a bump.
const Version = 1

// Store is the in-memory representation of sensitive-roots.json.
type Store struct {
	Version int    `json:"version"`
	Roots   []Root `json:"roots"`
}

// Root is one sensitive directory entry. Path is absolute and
// filepath.Clean-ed at the moment it was marked. MarkedAt is informational —
// printed by `yottacode sensitive list`, never consulted for gating.
type Root struct {
	Path     string    `json:"path"`
	MarkedAt time.Time `json:"marked_at"`
}

// DefaultStorePath returns ~/.yottacode/sensitive-roots.json.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "sensitive-roots.json"), nil
}

// Load reads the store at path. A missing file is the normal state for most
// users and returns an empty Store, not an error.
//
// A malformed file IS an error, and callers must not degrade to "no roots" on
// one: silently treating an unreadable store as empty would turn a typo into
// an unannounced loss of protection. Fail loudly so the user fixes it.
func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Store{Version: Version}, nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("sensitive: decode %s: %w", path, err)
	}
	if s.Version == 0 {
		s.Version = Version
	}
	return &s, nil
}

// Save writes s to path atomically (temp + rename), creating the parent dir at
// 0700 if missing. The file is 0644 — these are preferences, not secrets; the
// protection comes from the deny-list, not from file mode.
func Save(path string, s *Store) error {
	if s.Version == 0 {
		s.Version = Version
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sensitive-roots-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Contains reports whether path equals or is descended from any sensitive
// root. Descendant inheritance is what makes marking a repo root enough to
// cover every subdirectory a session might have started in.
func (s *Store) Contains(path string) bool {
	if s == nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	for _, r := range s.Roots {
		if pathUnder(abs, r.Path) {
			return true
		}
	}
	return false
}

// Paths returns every recorded root path. Used to build the outbound exclusion
// predicate, which must consider all sensitive roots — not just the current
// project's — so another repo's PHI can't surface here.
func (s *Store) Paths() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Roots))
	for _, r := range s.Roots {
		out = append(out, r.Path)
	}
	return out
}

// Add inserts a root if not already present by exact path match. Returns true
// if the entry is new. Adding /foo when /foo/bar is already marked records
// /foo too — the user has now named the broader root explicitly.
func (s *Store) Add(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	abs = filepath.Clean(abs)
	for _, r := range s.Roots {
		if r.Path == abs {
			return false, nil
		}
	}
	s.Roots = append(s.Roots, Root{Path: abs, MarkedAt: time.Now().UTC()})
	return true, nil
}

// Remove deletes a root by exact-path match. Returns true if an entry was
// removed. Exact match, not descendant match: removing /foo/bar must not
// silently strip protection from the broader /foo entry that also covers it.
func (s *Store) Remove(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	abs = filepath.Clean(abs)
	for i, r := range s.Roots {
		if r.Path == abs {
			s.Roots = append(s.Roots[:i], s.Roots[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// Clear empties the store.
func (s *Store) Clear() { s.Roots = nil }

// pathUnder reports whether descendant is at or below ancestor on the
// filesystem tree. Duplicates the helper in internal/trust and
// internal/agent/writepath.go to keep this package dependency-free; the
// function is pure and stable.
func pathUnder(descendant, ancestor string) bool {
	if ancestor == "" {
		return false
	}
	a, err := filepath.Abs(ancestor)
	if err != nil {
		return false
	}
	a = filepath.Clean(a)
	d := filepath.Clean(descendant)
	if d == a {
		return true
	}
	rel, err := filepath.Rel(a, d)
	if err != nil {
		return false
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return false
	}
	return true
}
