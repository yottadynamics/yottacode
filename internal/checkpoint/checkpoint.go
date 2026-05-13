// Package checkpoint implements the file + conversation snapshot store
// behind the TUI's /rewind command and Esc Esc keybinding.
//
// One checkpoint is created per user message: each contains a clone of
// the conversation history at trigger time and a list of files mutated
// during the turn. File contents are stored as content-addressed blobs
// shared across checkpoints in the same session, so a file edited in
// two consecutive turns stores only its two distinct pre-images.
//
// Layout on disk under ~/.yottacode/checkpoints/:
//
//	index.json                          # last-sweep timestamp
//	<session-id>/
//	  manifest.json                     # ordered list, cheap to read for the picker
//	  blobs/<sha256>.blob               # content-addressed pre-image bytes
//	  <checkpoint-id>/
//	    meta.json                       # prompt, user_msg_idx, []FileEntry
//	    messages.json                   # full Session.Messages slice at trigger time
//
// All writes use the .tmp + rename atomic pattern. SnapshotPath is
// idempotent: repeat calls for the same path within one checkpoint are
// no-ops, and the blob layer dedupes by sha so two checkpoints that
// snapshot identical bytes share one blob.
//
// Bash mutations and direct file edits made outside the registered
// Mutator tools are intentionally NOT tracked — mirrors Claude Code
// /rewind. The picker footer surfaces this so users aren't surprised.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// FileEntry records one path snapshotted into a checkpoint. ExistedAtCapture
// distinguishes "file existed pre-turn, restore its bytes" from "file
// did not exist pre-turn, delete it on restore". Mode preserves the
// permission bits (& 0o777; setuid/setgid stripped on purpose).
type FileEntry struct {
	Path              string      `json:"path"`
	SHA256            string      `json:"sha256,omitempty"`
	ExistedAtCapture  bool        `json:"existed"`
	Mode              os.FileMode `json:"mode"`
}

// Meta is the per-checkpoint metadata file. UserMsgIdx is the index
// into Session.Messages where the triggering user message will land
// (snapshot is taken before the message is appended).
type Meta struct {
	CheckpointID string      `json:"checkpoint_id"`
	SessionID    string      `json:"session_id"`
	Created      time.Time   `json:"created"`
	UserPrompt   string      `json:"user_prompt"`
	UserMsgIdx   int         `json:"user_msg_idx"`
	Files        []FileEntry `json:"files"`
}

// ManifestEntry is the cheap-to-load summary the picker reads. One per
// checkpoint, newest first. PromptPreview is truncated to ~200 chars.
type ManifestEntry struct {
	CheckpointID  string    `json:"checkpoint_id"`
	Created       time.Time `json:"created"`
	PromptPreview string    `json:"prompt_preview"`
}

// Manifest is the ordered list of checkpoints for one session.
type Manifest struct {
	SessionID string          `json:"session_id"`
	Entries   []ManifestEntry `json:"entries"`
}

// MessagesSnapshot is the format stored at <cp>/messages.json. Carries
// only the conversation slice — todos are out of scope for v1.
type MessagesSnapshot struct {
	SessionID string            `json:"session_id"`
	Captured  time.Time         `json:"captured"`
	Messages  []adapter.Message `json:"messages"`
}

// Store is the filesystem-backed checkpoint engine. One Store per yottacode
// run; methods are safe under concurrent SnapshotPath calls during a
// turn (Begin/Restore are not expected to be concurrent with themselves).
type Store struct {
	root string

	mu        sync.Mutex
	openMetas map[string]*Meta // checkpointID -> in-memory Meta during a turn
}

// DefaultRoot returns ~/.yottacode/checkpoints/. Created on demand by New.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "checkpoints"), nil
}

// New opens (or creates) a checkpoint store rooted at the given path.
// Passing "" defaults to ~/.yottacode/checkpoints/.
func New(root string) (*Store, error) {
	if root == "" {
		d, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = d
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{
		root:      root,
		openMetas: make(map[string]*Meta),
	}, nil
}

// Root returns the store's base directory. Useful for tests and the
// "where do checkpoints live?" footer in the picker.
func (s *Store) Root() string { return s.root }

func (s *Store) sessionDir(sessionID string) string {
	return filepath.Join(s.root, sessionID)
}

func (s *Store) blobsDir(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), "blobs")
}

func (s *Store) checkpointDir(sessionID, cpID string) string {
	return filepath.Join(s.sessionDir(sessionID), cpID)
}

func (s *Store) manifestPath(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), "manifest.json")
}

// Begin creates a new checkpoint for the given session before the user
// message at userMsgIdx is appended. Stores the conversation pre-image
// (history) and an initial empty FileEntry list. Returns the new
// checkpoint id.
//
// The picker uses the manifest entry's PromptPreview to label rows,
// derived from userPrompt by truncating to 200 chars.
func (s *Store) Begin(sessionID, userPrompt string, userMsgIdx int, history []adapter.Message) (string, error) {
	if sessionID == "" {
		return "", errors.New("checkpoint: sessionID required")
	}
	cpID := newCheckpointID(time.Now())
	cpDir := s.checkpointDir(sessionID, cpID)
	if err := os.MkdirAll(cpDir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.blobsDir(sessionID), 0o700); err != nil {
		return "", err
	}
	meta := &Meta{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Created:      time.Now().UTC(),
		UserPrompt:   userPrompt,
		UserMsgIdx:   userMsgIdx,
		Files:        nil,
	}
	if err := writeJSONAtomic(filepath.Join(cpDir, "meta.json"), meta); err != nil {
		return "", err
	}
	snap := MessagesSnapshot{
		SessionID: sessionID,
		Captured:  meta.Created,
		Messages:  cloneMessages(history),
	}
	if err := writeJSONAtomic(filepath.Join(cpDir, "messages.json"), snap); err != nil {
		return "", err
	}
	if err := s.appendManifest(sessionID, ManifestEntry{
		CheckpointID:  cpID,
		Created:       meta.Created,
		PromptPreview: previewPrompt(userPrompt),
	}); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.openMetas[cpID] = meta
	s.mu.Unlock()
	return cpID, nil
}

// SnapshotPath captures a pre-image of absPath into the checkpoint,
// appending a FileEntry to its meta.json. Idempotent on repeat calls
// for the same path within one checkpoint. Soft on missing file:
// records ExistedAtCapture=false so restore deletes it.
//
// Returns nil on success. Callers should log but not block on errors —
// snapshot failures must not abort the user's tool call.
func (s *Store) SnapshotPath(sessionID, cpID, absPath string) error {
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("checkpoint: SnapshotPath requires absolute path, got %q", absPath)
	}
	s.mu.Lock()
	meta, ok := s.openMetas[cpID]
	if !ok {
		s.mu.Unlock()
		// Late snapshot after Begin's in-memory entry was evicted (turn
		// ended) — fall back to disk read so a follow-up tool call in
		// the same turn still captures.
		loaded, err := s.loadMeta(sessionID, cpID)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.openMetas[cpID] = loaded
		meta = loaded
	}
	for _, f := range meta.Files {
		if f.Path == absPath {
			s.mu.Unlock()
			return nil // already captured this path for this checkpoint
		}
	}
	s.mu.Unlock()

	entry := FileEntry{Path: absPath}
	info, err := os.Stat(absPath)
	switch {
	case err == nil && !info.IsDir():
		bytes, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("checkpoint: read %s: %w", absPath, err)
		}
		sum := sha256.Sum256(bytes)
		hexSum := hex.EncodeToString(sum[:])
		if err := s.writeBlob(sessionID, hexSum, bytes); err != nil {
			return err
		}
		entry.SHA256 = hexSum
		entry.ExistedAtCapture = true
		entry.Mode = info.Mode().Perm() & 0o777
	case err == nil && info.IsDir():
		// Directories aren't restored; record nothing.
		return nil
	case errors.Is(err, os.ErrNotExist):
		entry.ExistedAtCapture = false
	default:
		return fmt.Errorf("checkpoint: stat %s: %w", absPath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the lock so two goroutines racing on the same
	// path don't both append. (Single-turn snapshot is sequential in
	// practice, but cheap to guard.)
	for _, f := range meta.Files {
		if f.Path == absPath {
			return nil
		}
	}
	meta.Files = append(meta.Files, entry)
	return writeJSONAtomic(filepath.Join(s.checkpointDir(sessionID, cpID), "meta.json"), meta)
}

// LoadManifest reads the per-session manifest, newest first. Empty
// manifest (or missing session dir) returns an empty slice without
// error so the picker can show "no checkpoints yet".
func (s *Store) LoadManifest(sessionID string) (Manifest, error) {
	var m Manifest
	m.SessionID = sessionID
	b, err := os.ReadFile(s.manifestPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("checkpoint: parse manifest %s: %w", sessionID, err)
	}
	sort.Slice(m.Entries, func(i, j int) bool {
		return m.Entries[i].Created.After(m.Entries[j].Created)
	})
	return m, nil
}

// LoadMeta returns the meta.json for one checkpoint.
func (s *Store) LoadMeta(sessionID, cpID string) (Meta, error) {
	m, err := s.loadMeta(sessionID, cpID)
	if err != nil {
		return Meta{}, err
	}
	return *m, nil
}

// LoadMessages returns the conversation snapshot bound to one checkpoint.
func (s *Store) LoadMessages(sessionID, cpID string) ([]adapter.Message, error) {
	b, err := os.ReadFile(filepath.Join(s.checkpointDir(sessionID, cpID), "messages.json"))
	if err != nil {
		return nil, err
	}
	var snap MessagesSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("checkpoint: parse messages.json: %w", err)
	}
	return snap.Messages, nil
}

// RestoreCode writes each FileEntry's blob back to disk, or deletes the
// file when ExistedAtCapture is false. Two-phase: write every .tmp
// first, then rename pass — minimizes the window in which only some
// files are restored. Returns the list of errors encountered; nil on
// full success.
func (s *Store) RestoreCode(sessionID, cpID string) ([]error, error) {
	meta, err := s.loadMeta(sessionID, cpID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		path, tmp string
		entry     FileEntry
		remove    bool
	}
	var prepared []pending
	var errs []error
	for _, f := range meta.Files {
		if !f.ExistedAtCapture {
			prepared = append(prepared, pending{path: f.Path, remove: true, entry: f})
			continue
		}
		bytes, err := s.readBlob(sessionID, f.SHA256)
		if err != nil {
			errs = append(errs, fmt.Errorf("read blob for %s: %w", f.Path, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("mkdir for %s: %w", f.Path, err))
			continue
		}
		tmp := f.Path + ".yottacode-rewind.tmp"
		mode := f.Mode & 0o777
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(tmp, bytes, mode); err != nil {
			errs = append(errs, fmt.Errorf("write tmp for %s: %w", f.Path, err))
			continue
		}
		prepared = append(prepared, pending{path: f.Path, tmp: tmp, entry: f})
	}
	for _, p := range prepared {
		if p.remove {
			if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove %s: %w", p.path, err))
			}
			continue
		}
		if err := os.Rename(p.tmp, p.path); err != nil {
			errs = append(errs, fmt.Errorf("rename %s: %w", p.path, err))
			_ = os.Remove(p.tmp)
		}
	}
	if len(errs) > 0 {
		return errs, nil
	}
	return nil, nil
}

// Sweep removes manifest entries older than ttl, deletes their
// checkpoint dirs, and GCs orphaned blobs within each session. A
// session whose manifest becomes empty has its directory removed
// entirely. Returns the number of checkpoints removed.
//
// Sweep is opportunistic — callers should run it in a background
// goroutine on session open. Idempotent and safe to call frequently.
func (s *Store) Sweep(ttl time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-ttl)
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		n, err := s.sweepSession(sessionID, cutoff)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	_ = s.touchIndex()
	return removed, nil
}

func (s *Store) sweepSession(sessionID string, cutoff time.Time) (int, error) {
	man, err := s.LoadManifest(sessionID)
	if err != nil {
		return 0, err
	}
	var keep []ManifestEntry
	removed := 0
	for _, en := range man.Entries {
		if en.Created.Before(cutoff) {
			_ = os.RemoveAll(s.checkpointDir(sessionID, en.CheckpointID))
			removed++
			continue
		}
		keep = append(keep, en)
	}
	if len(keep) == 0 {
		// Session has no checkpoints left — wipe its directory whole.
		return removed, os.RemoveAll(s.sessionDir(sessionID))
	}
	man.Entries = keep
	if err := writeJSONAtomic(s.manifestPath(sessionID), man); err != nil {
		return removed, err
	}
	if err := s.gcOrphanBlobs(sessionID, keep); err != nil {
		return removed, err
	}
	return removed, nil
}

func (s *Store) gcOrphanBlobs(sessionID string, keep []ManifestEntry) error {
	referenced := make(map[string]bool)
	for _, en := range keep {
		meta, err := s.loadMeta(sessionID, en.CheckpointID)
		if err != nil {
			continue
		}
		for _, f := range meta.Files {
			if f.SHA256 != "" {
				referenced[f.SHA256] = true
			}
		}
	}
	dir := s.blobsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".blob") {
			continue
		}
		sum := strings.TrimSuffix(name, ".blob")
		if !referenced[sum] {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

func (s *Store) touchIndex() error {
	return writeJSONAtomic(filepath.Join(s.root, "index.json"), map[string]any{
		"last_sweep": time.Now().UTC(),
	})
}

func (s *Store) loadMeta(sessionID, cpID string) (*Meta, error) {
	b, err := os.ReadFile(filepath.Join(s.checkpointDir(sessionID, cpID), "meta.json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("checkpoint: parse meta.json: %w", err)
	}
	return &m, nil
}

func (s *Store) appendManifest(sessionID string, entry ManifestEntry) error {
	man, err := s.LoadManifest(sessionID)
	if err != nil {
		return err
	}
	man.SessionID = sessionID
	man.Entries = append(man.Entries, entry)
	return writeJSONAtomic(s.manifestPath(sessionID), man)
}

func newCheckpointID(now time.Time) string {
	// cp-<unix-nanos>; nanosecond precision makes ordering trivial and
	// the picker doesn't need a separate sequence number.
	return fmt.Sprintf("cp-%020d", now.UTC().UnixNano())
}

func previewPrompt(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func cloneMessages(in []adapter.Message) []adapter.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]adapter.Message, len(in))
	for i, m := range in {
		out[i] = m
		if len(m.ToolCalls) > 0 {
			tc := make([]adapter.ToolCall, len(m.ToolCalls))
			copy(tc, m.ToolCalls)
			out[i].ToolCalls = tc
		}
		if len(m.Citations) > 0 {
			c := make([]adapter.Citation, len(m.Citations))
			copy(c, m.Citations)
			out[i].Citations = c
		}
	}
	return out
}

func writeJSONAtomic(path string, payload any) error {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
