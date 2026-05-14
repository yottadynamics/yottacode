// Folder trust — the per-directory consent gate that fires before
// the TUI opens a session in a previously-unseen workspace.
// Mirrors Claude Code's workspace-trust dialog. See
// yottacode-roadmap/folder-trust.md for the design.
//
// Trust is user-scope: one file at ~/.yottacode/trusted-roots.json
// records every directory the user has explicitly accepted. A
// trusted root covers every descendant via pathUnder, so cloning a
// new repo under an already-trusted parent does not re-prompt.
//
// The store is intentionally narrow:
//
//   - JSON only. Matches the existing ~/.yottacode/ conventions
//     (auth/openai-auth.json, config.toml, etc.).
//   - No SQLite, no per-project index — trust is one decision per
//     absolute path, recorded once.
//   - Atomic write (temp + rename) so a crash mid-save can't
//     leave a half-written roots file that silently drops every
//     existing entry.
//   - User-writable but agent-deny-listed via DefaultDenyPaths so
//     the model's write tools can't self-grant trust.
package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvTrustAll, when set to "1", suppresses every trust prompt and
// proceeds as if cwd were trusted. CI escape hatch — does NOT
// write anything to trusted-roots.json, because CI invocations
// shouldn't silently grow a user's persistent trust set.
const EnvTrustAll = "YOTTACODE_TRUST_ALL"

// Version is the on-disk schema version. Bumped only on a
// breaking change; readers tolerate unknown fields so additive
// changes don't require a bump.
const Version = 1

// Store is the in-memory representation of trusted-roots.json.
type Store struct {
	Version int    `json:"version"`
	Roots   []Root `json:"roots"`
}

// Root is one trusted directory entry. Path is absolute and
// filepath.Clean-ed at the moment the user said Yes. TrustedAt
// is informational — printed by `yottacode trust list`, not
// consulted for any gating.
type Root struct {
	Path      string    `json:"path"`
	TrustedAt time.Time `json:"trusted_at"`
}

// DefaultStorePath returns ~/.yottacode/trusted-roots.json. The
// caller is expected to create the parent dir if writing; Load
// tolerates a missing file by returning an empty Store.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "trusted-roots.json"), nil
}

// Load reads the store at path. A missing file is a normal first-
// run state and returns an empty Store, not an error. A malformed
// file is an error so the user can fix it instead of silently
// running with an erased trust set.
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
		return nil, fmt.Errorf("trust: decode %s: %w", path, err)
	}
	if s.Version == 0 {
		s.Version = Version
	}
	return &s, nil
}

// Save writes s to path atomically (temp + rename). Creates the
// parent dir at 0700 if missing. The file is 0644 — trust roots
// are not secrets, just user preferences.
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
	tmp, err := os.CreateTemp(dir, ".trusted-roots-*.tmp")
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

// Contains reports whether path equals or is descended from any
// trusted root. Path is absolutized; entries already are.
func (s *Store) Contains(path string) bool {
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

// Add inserts a root if not already present (by exact path
// match). Subfolder inheritance via Contains is intentional —
// adding /foo when /foo/bar is already trusted records /foo too,
// because the user has now explicitly named the broader root.
// Returns true if the entry is new.
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
	s.Roots = append(s.Roots, Root{Path: abs, TrustedAt: time.Now().UTC()})
	return true, nil
}

// Remove deletes a root by exact-path match. Returns true if an
// entry was removed.
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

// Clear empties the store. The next launch in every previously
// trusted directory will prompt again.
func (s *Store) Clear() {
	s.Roots = nil
}

// IsTrusted reports whether cwd is trusted under any of the
// signals that satisfy the gate:
//
//  1. cwd is in trusted-roots.json (exact or subfolder)
//  2. EnvTrustAll is set to "1" (CI escape hatch — does not
//     persist)
//  3. cwd is at or under any entry in allowPaths (passed via
//     --allow-paths or $YOTTACODE_ALLOW_PATHS — explicit
//     per-session declaration, also does not persist)
//
// Signals 2 and 3 do not write to the store; they expire when
// the env var or flag goes away.
func IsTrusted(s *Store, cwd string, allowPaths []string) bool {
	if os.Getenv(EnvTrustAll) == "1" {
		return true
	}
	if s != nil && s.Contains(cwd) {
		return true
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	for _, p := range allowPaths {
		if pathUnder(abs, p) {
			return true
		}
	}
	return false
}

// pathUnder reports whether descendant is at or below ancestor on
// the filesystem tree. Duplicates the helper in
// internal/agent/writepath.go to keep trust free of an agent
// import; the function is pure and stable.
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
