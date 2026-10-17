package codemap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/yottadynamics/yottacode/internal/lsp"
	"github.com/yottadynamics/yottacode/internal/syncutil"
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

// CachedProvider reuses the last index until supported source file paths,
// mtimes, sizes, or counts change. Without a running watch (see StartWatch)
// that check is a full workspace walk on every Index call; with one
// running, Index instead checks a cheap in-memory dirty flag and, when set,
// rebuilds the whole index. A rebuild is bounded by the file cap and the
// build time budget, so there is no incremental patch state to keep correct.
type CachedProvider struct {
	Options BuildOptions

	// RetryTimedOutAfter is how long a snapshot that stopped at the build time
	// budget is served before the next query rebuilds it. Zero means one minute.
	// A file-cap stop is not retried: the same files would hit the same cap.
	RetryTimedOutAfter time.Duration

	mu          syncutil.Mutex
	snapshot    *CodeIndex
	builtAt     time.Time
	fingerprint workspaceFingerprint

	watchStarted atomic.Bool
	dirty        atomic.Bool
	watchCancel  context.CancelFunc
}

func (p *CachedProvider) Index(ctx context.Context) (*CodeIndex, error) {
	if p.watchStarted.Load() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.snapshot != nil && !p.dirty.Load() && !p.timedOutSnapshotExpired() {
			return p.snapshot, nil
		}
		// Clear the flag before building so a change that lands mid-build marks
		// the next snapshot stale again instead of being lost.
		p.dirty.Store(false)
		idx, err := Build(ctx, p.Options)
		if err != nil {
			p.dirty.Store(true)
			return nil, err
		}
		p.snapshot, p.builtAt = idx, time.Now()
		return idx, nil
	}
	fp, err := fingerprintWorkspace(ctx, p.Options.Root, p.Options.MaxFiles)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot != nil && p.fingerprint == fp && !p.timedOutSnapshotExpired() {
		return p.snapshot, nil
	}
	idx, err := Build(ctx, p.Options)
	if err != nil {
		return nil, err
	}
	p.snapshot, p.builtAt = idx, time.Now()
	p.fingerprint = fp
	return idx, nil
}

// timedOutSnapshotExpired reports whether the cached snapshot stopped at the
// build time budget and has been served long enough that a rebuild is worth
// trying: the stop usually reflects a loaded machine, not the repository. Must be
// called with p.mu held.
func (p *CachedProvider) timedOutSnapshotExpired() bool {
	if p.snapshot == nil || !p.snapshot.Info().TimedOut {
		return false
	}
	after := p.RetryTimedOutAfter
	if after <= 0 {
		after = time.Minute
	}
	return time.Since(p.builtAt) >= after
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
