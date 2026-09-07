package codemap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// Provider returns the latest code-map snapshot. TUI rebuilds can swap the
// snapshot while agent tools keep depending on this tiny read-only surface.
type Provider interface {
	Index(ctx context.Context) (*CodeIndex, error)
}

// StaticProvider returns one immutable index, useful in tests and one-shot runs.
type StaticProvider struct{ Snapshot *CodeIndex }

func (p StaticProvider) Index(context.Context) (*CodeIndex, error) { return p.Snapshot, nil }

// BuilderProvider rebuilds the index on demand.
type BuilderProvider struct{ Options BuildOptions }

func (p BuilderProvider) Index(ctx context.Context) (*CodeIndex, error) { return Build(ctx, p.Options) }

// maxIncrementalChanges bounds how many pending paths applyChanges will
// patch into the persistent buildState in one go. A debounced batch larger
// than this (e.g. a branch switch touching hundreds of files at once) falls
// back to a full Build instead — simpler and safer than a long incremental
// patch, and the persistent state gets rebuilt fresh either way.
const maxIncrementalChanges = 50

// CachedProvider reuses the last index until supported source file paths,
// mtimes, sizes, or counts change. Without a running watch (see StartWatch)
// that check is a full workspace walk on every Index call; with one
// running, Index instead checks a cheap in-memory dirty flag and, when set,
// patches only the specific paths the watcher observed changing into a
// persistent buildState (see applyChanges) rather than re-walking and
// re-parsing the whole workspace.
type CachedProvider struct {
	Options BuildOptions

	mu          sync.Mutex
	snapshot    *CodeIndex
	fingerprint workspaceFingerprint
	state       *buildState // persistent parsed state once a watch has produced at least one snapshot; nil otherwise

	watchStarted atomic.Bool
	dirty        atomic.Bool
	watchCancel  context.CancelFunc

	pendingMu sync.Mutex
	pending   map[string]bool // absolute paths the watcher has observed changing since the last applied update
}

func (p *CachedProvider) Index(ctx context.Context) (*CodeIndex, error) {
	if p.watchStarted.Load() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.snapshot != nil && !p.dirty.Load() {
			return p.snapshot, nil
		}
		idx, err := p.rebuildLocked(ctx)
		if err != nil {
			return nil, err
		}
		p.dirty.Store(false)
		return idx, nil
	}
	fp, err := fingerprintWorkspace(ctx, p.Options.Root, p.Options.MaxFiles)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot != nil && p.fingerprint == fp {
		return p.snapshot, nil
	}
	idx, err := Build(ctx, p.Options)
	if err != nil {
		return nil, err
	}
	p.snapshot = idx
	p.fingerprint = fp
	return idx, nil
}

// rebuildLocked must be called with p.mu held. It applies any pending
// watch-observed changes incrementally against the persistent buildState
// when one exists and the changeset is small enough, falling back to a full
// Build (and a fresh buildState) otherwise — the very first build always
// takes this path, since there's no prior state yet to patch.
func (p *CachedProvider) rebuildLocked(ctx context.Context) (*CodeIndex, error) {
	pending := p.drainPending()
	if p.state != nil && len(pending) <= maxIncrementalChanges {
		if remaining := p.state.applyChanges(ctx, pending); len(remaining) > 0 {
			// ctx was canceled partway through: hand the unprocessed paths
			// back to pending instead of silently losing them — drainPending
			// already removed them from p.pending, and nothing else records
			// which files still need re-indexing once this call gives up.
			p.requeuePending(remaining)
			return nil, ctx.Err()
		}
		idx := p.state.finalize()
		p.snapshot = idx
		return idx, nil
	}
	st, err := newBuildState(p.Options)
	if err != nil {
		p.requeuePending(pending)
		return nil, err
	}
	if err := st.walkAll(ctx); err != nil {
		// The old state/snapshot are untouched (st is a fresh local value,
		// only ever published to p.state below on success), so the pending
		// paths that triggered this attempt need to survive for the retry.
		p.requeuePending(pending)
		return nil, err
	}
	idx := st.finalize()
	p.state = st
	p.snapshot = idx
	return idx, nil
}

func (p *CachedProvider) addPending(path string) {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	if p.pending == nil {
		p.pending = map[string]bool{}
	}
	p.pending[path] = true
}

func (p *CachedProvider) drainPending() map[string]bool {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	out := p.pending
	p.pending = nil
	return out
}

func (p *CachedProvider) requeuePending(paths map[string]bool) {
	if len(paths) == 0 {
		return
	}
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	if p.pending == nil {
		p.pending = map[string]bool{}
	}
	for path := range paths {
		p.pending[path] = true
	}
}

// applyChanges patches each pending absolute path into st using its CURRENT
// on-disk state rather than replaying the specific fsnotify event kind that
// triggered it — a debounced batch commonly coalesces several events into
// one net effect (e.g. create-then-write), so checking what's actually
// there now is simpler and more correct than reconstructing intent from a
// sequence of events. If ctx is canceled partway through, the (unmodified)
// remaining entries of pending are returned so the caller can requeue them
// rather than lose them; a nil/empty return means every path was applied.
func (st *buildState) applyChanges(ctx context.Context, pending map[string]bool) map[string]bool {
	for absPath := range pending {
		if ctx.Err() != nil {
			return pending
		}
		delete(pending, absPath)
		rel := relPath(st.absRoot, absPath)
		info, err := os.Stat(absPath)
		if err != nil {
			st.removeFileTracked(rel)
			st.removeDirRecursive(rel)
			continue
		}
		if info.IsDir() {
			// (Re-)walk it fresh: a directory can appear with files already
			// inside (e.g. `mv` of a whole subtree, or a git checkout) without
			// fsnotify firing an individual event per contained file.
			if err := st.walkSubtree(ctx, absPath); err != nil {
				// walkSubtree only ever returns non-nil for ctx cancellation
				// (its own WalkDir already swallows ordinary per-file walk
				// errors) — put this directory back so a retry finishes
				// indexing it instead of leaving it silently half-applied.
				pending[absPath] = true
				return pending
			}
			continue
		}
		st.upsertFile(ctx, absPath)
	}
	return nil
}

// maxWatchedDirs bounds how many directories StartWatch will register with
// the OS watcher. Beyond this, a single fsnotify instance risks the
// platform's inotify/kqueue watch-descriptor limit on very large repos, so
// StartWatch backs off entirely and Index keeps using the fingerprint path.
const maxWatchedDirs = 4000

// StartWatch enables watcher-backed cache invalidation. It is a soft-failure
// feature: an oversized repo, a fsnotify.NewWatcher error (e.g. a platform
// without inotify/kqueue, or the OS watch-descriptor limit), or any other
// setup problem simply leaves watchStarted false, and Index falls back to
// exactly today's per-call fingerprint walk. The returned error is only
// non-nil for a bad Options.Root; callers can safely ignore it and keep
// using the provider either way.
func (p *CachedProvider) StartWatch(ctx context.Context) error {
	if p.watchStarted.Load() {
		return nil
	}
	root := p.Options.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dirs, ok := collectWatchDirs(absRoot, maxWatchedDirs)
	if !ok || len(dirs) == 0 {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	for _, dir := range dirs {
		_ = watcher.Add(dir)
	}
	debounce := p.Options.WatchDebounce
	if debounce <= 0 {
		debounce = 300 * time.Millisecond
	}
	watchCtx, cancel := context.WithCancel(ctx)
	p.watchCancel = cancel
	p.watchStarted.Store(true)
	go p.watchLoop(watchCtx, watcher, debounce)
	return nil
}

// Close stops the watch goroutine started by StartWatch. It is a no-op when
// StartWatch was never called or never actually started a watch.
func (p *CachedProvider) Close() {
	if p.watchCancel != nil {
		p.watchCancel()
	}
}

func (p *CachedProvider) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, debounce time.Duration) {
	defer watcher.Close()
	var debounceTimer *time.Timer
	scheduleDirty := func() {
		if debounceTimer == nil {
			debounceTimer = time.AfterFunc(debounce, func() { p.dirty.Store(true) })
			return
		}
		debounceTimer.Reset(debounce)
	}
	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !relevantWatchEvent(event) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() && !shouldSkipDir(filepath.Base(event.Name)) {
					_ = watcher.Add(event.Name)
				}
			}
			if event.Op&fsnotify.Remove != 0 {
				_ = watcher.Remove(event.Name)
			}
			p.addPending(event.Name)
			scheduleDirty()
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// relevantWatchEvent filters fsnotify events down to ones worth invalidating
// the cache for. Structural changes (create/remove/rename) always count,
// since a new/removed path could be a directory needing a watch update even
// before we know its kind; a plain write only counts for tracked source or
// doc files, so writes to build artifacts and other unindexed files in a
// watched directory don't trigger needless rebuilds.
func relevantWatchEvent(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		return true
	}
	if event.Op&fsnotify.Write == 0 {
		return false
	}
	if _, ok := lsp.ResolveFile(event.Name); ok {
		return true
	}
	return isDocFile(event.Name)
}

// collectWatchDirs walks root the same way Build does (skipping the same
// directories) and returns every directory path. ok is false when the walk
// fails or the directory count exceeds max, signaling the caller to fall
// back rather than register a partial/oversized watch.
func collectWatchDirs(root string, max int) (dirs []string, ok bool) {
	ok = true
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		if len(dirs) > max {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return dirs, ok
}

type workspaceFingerprint struct {
	Files     int
	LatestMod int64
	TotalSize int64
}

func fingerprintWorkspace(ctx context.Context, root string, maxFiles int) (workspaceFingerprint, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return workspaceFingerprint{}, err
	}
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	var fp workspaceFingerprint
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == absRoot {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := lsp.ResolveFile(path); !ok && !isDocFile(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fp.Files++
		fp.TotalSize += info.Size()
		if mod := info.ModTime().UTC().Round(time.Nanosecond).UnixNano(); mod > fp.LatestMod {
			fp.LatestMod = mod
		}
		if fp.Files > maxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return workspaceFingerprint{}, err
	}
	return fp, nil
}
