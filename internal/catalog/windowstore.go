package catalog

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The context-window store is the file-backed fallback table that
// replaces the old hardcoded Go map. It has two tiers:
//
//   - an EMBEDDED baseline (context-windows.json, committed and compiled
//     into the binary exactly like catalog.gen.json) so a fresh, offline
//     install still resolves windows out of the box. Regenerate it with
//     `yottacode model probe-windows --output internal/catalog/context-windows.json`
//     before publishing.
//   - a runtime OVERLAY at ~/.yottacode/context-windows.json that takes
//     precedence, written by the TUI's background probe and by
//     `yottacode model probe-windows` (no --output) so discovered,
//     per-deployment windows accumulate across sessions and users can
//     hand-edit.
//
// ResolveWindow consults the store via WindowFor, between the curated
// catalog and the default floor. "prefix" matches the model id case-
// insensitively; an exact id is just a full-length prefix. Lookup prefers
// the LONGEST matching prefix so a specific id wins over a broad family
// entry regardless of file order, and the overlay wins over the baseline.

//go:embed context-windows.json
var embeddedWindowsJSON []byte

// WindowStoreEntry is one row of the context-window store.
type WindowStoreEntry struct {
	Prefix string `json:"prefix"`
	Window int    `json:"window"`
}

// windowStoreFile is the on-disk / embedded shape. Comment is ignored on
// read; kept so the embedded file can carry a regenerate hint.
type windowStoreFile struct {
	Comment     string             `json:"_comment,omitempty"`
	GeneratedAt time.Time          `json:"generated_at,omitempty"`
	Entries     []WindowStoreEntry `json:"entries"`
}

// windowStorePathFn resolves the runtime overlay path. A var so tests can
// redirect it to a temp file without touching the real home directory.
var windowStorePathFn = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "context-windows.json"), nil
}

var (
	windowStoreMu       sync.RWMutex
	windowBaselineCache []WindowStoreEntry // embedded, sorted longest-prefix-first
	windowOverlayCache  []WindowStoreEntry // runtime overlay, sorted longest-prefix-first
	windowStoreLoaded   bool
)

// loadWindowStore loads (once) and returns the overlay and baseline entry
// sets, each sorted longest-prefix-first. A missing/malformed overlay
// yields an empty overlay; a malformed embedded baseline yields none
// (the table is best-effort, never fatal).
func loadWindowStore() (overlay, baseline []WindowStoreEntry) {
	windowStoreMu.RLock()
	if windowStoreLoaded {
		defer windowStoreMu.RUnlock()
		return windowOverlayCache, windowBaselineCache
	}
	windowStoreMu.RUnlock()

	windowStoreMu.Lock()
	defer windowStoreMu.Unlock()
	if !windowStoreLoaded {
		windowBaselineCache = sortByPrefixLen(parseWindowEntries(embeddedWindowsJSON))
		windowOverlayCache = sortByPrefixLen(readOverlayEntries())
		windowStoreLoaded = true
	}
	return windowOverlayCache, windowBaselineCache
}

// readOverlayEntries reads ~/.yottacode/context-windows.json (no caching).
func readOverlayEntries() []WindowStoreEntry {
	path, err := windowStorePathFn()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // missing overlay is the normal state
	}
	return parseWindowEntries(raw)
}

// parseWindowEntries unmarshals a window store payload, dropping blank or
// non-positive rows and lowercasing prefixes for case-insensitive match.
func parseWindowEntries(raw []byte) []WindowStoreEntry {
	if len(raw) == 0 {
		return nil
	}
	var f windowStoreFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	out := make([]WindowStoreEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		if strings.TrimSpace(e.Prefix) == "" || e.Window <= 0 {
			continue
		}
		out = append(out, WindowStoreEntry{Prefix: strings.ToLower(e.Prefix), Window: e.Window})
	}
	return out
}

// sortByPrefixLen orders entries longest-prefix-first so the most specific
// match wins at lookup.
func sortByPrefixLen(entries []WindowStoreEntry) []WindowStoreEntry {
	sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].Prefix) > len(entries[j].Prefix) })
	return entries
}

// windowStoreLookup returns the window for a model from the store, matching
// the longest prefix and preferring the runtime overlay over the embedded
// baseline. ok is false when nothing matches. tag must already be
// lowercased.
func windowStoreLookup(tag string) (int, bool) {
	overlay, baseline := loadWindowStore()
	for _, set := range [][]WindowStoreEntry{overlay, baseline} {
		for _, e := range set {
			if strings.HasPrefix(tag, e.Prefix) {
				return e.Window, true
			}
		}
	}
	return 0, false
}

// UpsertWindow records window for an exact model id in the runtime overlay
// (~/.yottacode/context-windows.json), writing it atomically and refreshing
// the cache. An existing entry whose prefix equals model is updated;
// otherwise a new exact-id entry is added. window <= 0 or an empty model is
// a no-op. This is the write side used by the TUI's background probe (so a
// discovered window persists for later sessions) and by `model
// probe-windows` without an explicit output path.
func UpsertWindow(model string, window int) error {
	id := strings.TrimSpace(model)
	if id == "" || window <= 0 {
		return nil
	}
	path, err := windowStorePathFn()
	if err != nil {
		return err
	}

	windowStoreMu.Lock()
	defer windowStoreMu.Unlock()

	// Read the current overlay fresh (another process may have written it),
	// upsert, write back. Match on the lowercased id so we don't add a
	// duplicate that differs only in case.
	f := windowStoreFile{}
	if raw, rerr := os.ReadFile(path); rerr == nil {
		_ = json.Unmarshal(raw, &f) // best-effort; a corrupt file is replaced
	}
	lid := strings.ToLower(id)
	found := false
	for i := range f.Entries {
		if strings.ToLower(f.Entries[i].Prefix) == lid {
			f.Entries[i].Window = window
			found = true
			break
		}
	}
	if !found {
		f.Entries = append(f.Entries, WindowStoreEntry{Prefix: id, Window: window})
	}
	f.GeneratedAt = time.Now().UTC()
	if err := writeWindowStoreFile(path, f); err != nil {
		return err
	}
	windowStoreLoaded = false // invalidate so the next lookup re-reads
	return nil
}

// LoadEntriesFromFile reads and parses the window entries from a specific
// file (e.g. the committed baseline or the runtime overlay), for tooling
// that regenerates the store and wants to merge with what's already there.
// Returns nil for a missing or malformed file.
func LoadEntriesFromFile(path string) []WindowStoreEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseWindowEntries(raw)
}

// WriteWindowStore replaces a window-store file at path with the given
// entries (full rewrite, not an upsert) — used by `model probe-windows` to
// (re)generate either the runtime overlay or, with --output, the committed
// embedded baseline. Pass an empty path to target the runtime overlay.
func WriteWindowStore(path string, entries []WindowStoreEntry, comment string) error {
	if strings.TrimSpace(path) == "" {
		p, err := windowStorePathFn()
		if err != nil {
			return err
		}
		path = p
	}
	f := windowStoreFile{Comment: comment, GeneratedAt: time.Now().UTC(), Entries: entries}
	if err := writeWindowStoreFile(path, f); err != nil {
		return err
	}
	windowStoreMu.Lock()
	windowStoreLoaded = false
	windowStoreMu.Unlock()
	return nil
}

// writeWindowStoreFile writes f to path atomically (temp + rename) under a
// private mode, creating the parent dir if needed.
func writeWindowStoreFile(path string, f windowStoreFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
