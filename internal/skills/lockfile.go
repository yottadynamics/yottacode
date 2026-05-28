package skills

// Phase 2 provenance: ~/.yottacode/skills/.lock.json records where
// every user-installed skill came from, when it was installed, the
// content hash at install time, and a placeholder trust tag.
//
// The lockfile is loaded on demand (by check/update), not at session
// start — the runtime never blocks startup on it. The skill registry
// (registry.go:loadDir) already skips dot-prefixed entries, so the
// lockfile sitting alongside the per-skill dirs is invisible to the
// normal scan.
//
// Schema is forward-compatible: a Version field lets future
// migrations spot old layouts; unknown JSON fields are ignored on
// read so a future Phase 3 (Ed25519 signatures) can add fields
// without breaking older binaries.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LockfileName is the on-disk filename inside DestRoot. Dot-prefixed
// so registry.loadDir's existing hidden-entry skip already excludes
// it from skill enumeration.
const LockfileName = ".lock.json"

// CurrentLockfileVersion is the schema version this binary writes.
// Loaders accept any version <= this; newer files are still parsed
// but a warning is logged so users know their newer client wrote it.
const CurrentLockfileVersion = 1

// LockEntry is one installed skill's provenance row. Fields mirror
// the bullet list in the Phase 2 design notes verbatim.
type LockEntry struct {
	Name        string     `json:"name"`
	SourceType  SourceType `json:"source_type"`
	Source      string     `json:"source"`
	Hash        string     `json:"hash"`
	InstalledAt time.Time  `json:"installed_at"`
	Trust       string     `json:"trust"`
}

// Lockfile is the top-level JSON document. Entries are keyed by skill
// name so lookups are O(1) and order on disk is stable.
type Lockfile struct {
	Version int                  `json:"version"`
	Entries map[string]LockEntry `json:"entries"`
}

// newLockfile returns an empty lockfile at the current schema
// version. Used internally by LoadLockfile when no file exists; also
// exported intent for callers that want to compose entries before
// any save (currently no such caller — kept package-private).
func newLockfile() *Lockfile {
	return &Lockfile{
		Version: CurrentLockfileVersion,
		Entries: map[string]LockEntry{},
	}
}

// LoadLockfile reads <destRoot>/.lock.json. Missing file is not an
// error — first-time install will Save() and create it. A malformed
// file is an error so we never silently overwrite half-broken state.
func LoadLockfile(destRoot string) (*Lockfile, error) {
	path := filepath.Join(destRoot, LockfileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newLockfile(), nil
		}
		return nil, fmt.Errorf("read lockfile %s: %w", path, err)
	}
	lf := newLockfile()
	if err := json.Unmarshal(data, lf); err != nil {
		return nil, fmt.Errorf("parse lockfile %s: %w", path, err)
	}
	if lf.Entries == nil {
		lf.Entries = map[string]LockEntry{}
	}
	return lf, nil
}

// Save writes the lockfile atomically (tempfile + rename). Caller is
// responsible for ensuring destRoot exists.
func (l *Lockfile) Save(destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		return err
	}
	path := filepath.Join(destRoot, LockfileName)
	tmp, err := os.CreateTemp(destRoot, ".lock-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		// Clean up the temp file if the rename never happened.
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(l); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Set inserts or replaces an entry. Mutates the receiver in place;
// caller must Save() to persist.
func (l *Lockfile) Set(entry LockEntry) {
	if l.Entries == nil {
		l.Entries = map[string]LockEntry{}
	}
	l.Entries[entry.Name] = entry
}

// Remove drops an entry by name. No-op when the entry doesn't exist.
func (l *Lockfile) Remove(name string) {
	if l.Entries != nil {
		delete(l.Entries, name)
	}
}

// Get looks up an entry by name. The bool mirrors map-lookup
// convention so callers can distinguish "missing" from "zero value."
func (l *Lockfile) Get(name string) (LockEntry, bool) {
	if l.Entries == nil {
		return LockEntry{}, false
	}
	e, ok := l.Entries[name]
	return e, ok
}

// AllSorted returns entries in name order. Used by `skills check` /
// `skills update` (no-name forms) so output is deterministic.
func (l *Lockfile) AllSorted() []LockEntry {
	out := make([]LockEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TrustUnverified is the v2 trust tag. Every installed entry receives
// it; the field is reserved for a future signing/trust subsystem
// (Ed25519 verification, trusted-publisher allowlists) that can
// upgrade installed entries without rewriting the lockfile schema.
const TrustUnverified = "unverified"
