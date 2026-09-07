package codemap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectWatchDirsCapsAtMax(t *testing.T) {
	root := t.TempDir()
	for i := range 5 {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if dirs, ok := collectWatchDirs(root, 3); ok {
		t.Fatalf("expected ok=false when dir count exceeds max, got dirs=%v", dirs)
	}
	dirs, ok := collectWatchDirs(root, 100)
	if !ok || len(dirs) != 6 { // root + 5 subdirs
		t.Fatalf("collectWatchDirs = %v ok=%v, want 6 dirs ok=true", dirs, ok)
	}
}

func TestCollectWatchDirsSkipsSameDirsAsBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs, ok := collectWatchDirs(root, 100)
	if !ok {
		t.Fatal("expected ok=true")
	}
	for _, d := range dirs {
		if filepath.Base(d) == "pkg" {
			t.Fatalf("node_modules subtree should not be watched: %v", dirs)
		}
	}
}

// TestCachedProviderWatchDebouncesBeforeMarkingDirty verifies the watch path
// is actually engaged (not just that a change is eventually noticed, which
// the pre-existing fingerprint path already does): inside the debounce
// window Index must keep serving the stale snapshot, and only rebuild once
// the debounce has elapsed and the watcher marked the cache dirty.
func TestCachedProviderWatchDebouncesBeforeMarkingDirty(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	provider := &CachedProvider{Options: BuildOptions{Root: root, WatchDebounce: 200 * time.Millisecond}}
	ctx := context.Background()

	first, err := provider.Index(ctx)
	if err != nil {
		t.Fatalf("first Index: %v", err)
	}
	if err := provider.StartWatch(ctx); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(provider.Close)

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc B() {}\n")
	// Give fsnotify a moment to deliver the event, but stay well inside the
	// debounce window so the snapshot must not have flipped yet.
	time.Sleep(20 * time.Millisecond)
	stillCached, err := provider.Index(ctx)
	if err != nil {
		t.Fatalf("Index during debounce window: %v", err)
	}
	if stillCached != first {
		t.Fatal("Index should keep serving the cached snapshot inside the debounce window")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		updated, err := provider.Index(ctx)
		if err != nil {
			t.Fatalf("Index after debounce: %v", err)
		}
		if updated != first {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("watch never triggered a rebuild after the debounce window")
}

// TestCachedProviderRealWatchAppliesIncrementalContentCorrectly closes the
// gap between the deterministic addPending-driven tests (which prove
// applyChanges itself is correct) and the debounce-timing test (which only
// checks a new snapshot appears): this goes through a real StartWatch and
// fsnotify event, and checks the resulting index's actual content.
func TestCachedProviderRealWatchAppliesIncrementalContentCorrectly(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	provider := &CachedProvider{Options: BuildOptions{Root: root, WatchDebounce: 50 * time.Millisecond}}
	ctx := context.Background()

	if _, err := provider.Index(ctx); err != nil {
		t.Fatalf("first Index: %v", err)
	}
	if err := provider.StartWatch(ctx); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(provider.Close)

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc RealWatchFn() {}\n")

	deadline := time.Now().Add(3 * time.Second)
	for {
		idx, err := provider.Index(ctx)
		if err != nil {
			t.Fatalf("Index: %v", err)
		}
		found := false
		for _, s := range idx.SymbolsForFile("a.go") {
			if s.Name == "RealWatchFn" {
				found = true
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("real watch never applied the file's new content")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCachedProviderStartWatchIsSafeWithoutIndexCall(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	provider := &CachedProvider{Options: BuildOptions{Root: root, WatchDebounce: 5 * time.Millisecond}}
	if err := provider.StartWatch(context.Background()); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(provider.Close)
	if _, err := provider.Index(context.Background()); err != nil {
		t.Fatalf("Index after StartWatch with no prior snapshot: %v", err)
	}
}

func TestCachedProviderCloseWithoutStartWatchIsNoop(t *testing.T) {
	provider := &CachedProvider{Options: BuildOptions{Root: t.TempDir()}}
	provider.Close() // must not panic
}

// newIncrementalTestProvider simulates a running watch deterministically,
// without real fsnotify or debounce timing: setting watchStarted before the
// first Index call routes it through rebuildLocked, which — since p.state
// starts nil — does a full build and seeds the persistent state. Tests then
// drive the incremental path directly via addPending + dirty, exercising
// exactly the same rebuildLocked/applyChanges code the real watch loop
// calls, deterministically.
func newIncrementalTestProvider(t *testing.T, opts BuildOptions) *CachedProvider {
	t.Helper()
	p := &CachedProvider{Options: opts}
	p.watchStarted.Store(true)
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatalf("initial Index: %v", err)
	}
	return p
}

func triggerIncremental(t *testing.T, p *CachedProvider, absPaths ...string) *CodeIndex {
	t.Helper()
	for _, path := range absPaths {
		p.addPending(path)
	}
	p.dirty.Store(true)
	idx, err := p.Index(context.Background())
	if err != nil {
		t.Fatalf("incremental Index: %v", err)
	}
	return idx
}

func TestCachedProviderIncrementalUpdateOnlyReprocessesChangedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	write(t, root, "b.go", "package main\nfunc B() {}\n")
	source := &countingSource{}
	p := newIncrementalTestProvider(t, BuildOptions{Root: root, Source: source})
	if source.calls != 2 {
		t.Fatalf("initial build calls = %d, want 2 (a.go + b.go)", source.calls)
	}

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc A2() {}\n")
	first, _ := p.Index(context.Background())
	second := triggerIncremental(t, p, filepath.Join(root, "a.go"))
	if second == first {
		t.Fatal("changed file should produce a new snapshot")
	}
	if source.calls != 3 {
		t.Fatalf("post-incremental calls = %d, want 3 (only a.go re-symbolized, b.go left alone)", source.calls)
	}
}

func TestCachedProviderIncrementalUpdatePicksUpNewSymbols(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc A2() {}\n")
	idx := triggerIncremental(t, p, filepath.Join(root, "a.go"))
	syms := idx.SymbolsForFile("a.go")
	found := false
	for _, s := range syms {
		if s.Name == "A2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected A2 among a.go's symbols after incremental update: %+v", syms)
	}
}

func TestCachedProviderIncrementalUpdateAddsNewFileAndEdge(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc Use() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() { lib.Use() }\n")
	idx := triggerIncremental(t, p, filepath.Join(root, "internal/app/app.go"))
	if _, ok := findNode(idx, NodeFile, "internal/app/app.go", "app.go"); !ok {
		t.Fatalf("new file should be indexed: %+v", idx.Nodes())
	}
	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("new file's import edge = %+v, want internal/lib/lib.go", deps)
	}
}

func TestCachedProviderIncrementalUpdateRemovesDeletedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	write(t, root, "b.go", "package main\nfunc B() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	idx := triggerIncremental(t, p, filepath.Join(root, "b.go"))
	if _, ok := findNode(idx, NodeFile, "b.go", "b.go"); ok {
		t.Fatalf("removed file should no longer be indexed: %+v", idx.Nodes())
	}
	if _, ok := findNode(idx, NodeFile, "a.go", "a.go"); !ok {
		t.Fatal("unrelated file should remain indexed")
	}
}

func TestCachedProviderIncrementalUpdatePicksUpNewDirectoryWithFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	// Simulate a directory appearing with files already inside it (e.g. `mv`
	// of a whole subtree) — fsnotify fires one Create for the directory, not
	// one per contained file, so the pending path is the directory itself.
	newDir := filepath.Join(root, "moved")
	write(t, root, "moved/x.go", "package moved\nfunc X() {}\n")
	idx := triggerIncremental(t, p, newDir)
	if _, ok := findNode(idx, NodeFile, "moved/x.go", "x.go"); !ok {
		t.Fatalf("files inside a newly-created directory should be indexed: %+v", idx.Nodes())
	}
}

func TestCachedProviderIncrementalUpdateRemovesDeletedDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	write(t, root, "sub/x.go", "package sub\nfunc X() {}\n")
	write(t, root, "sub/y.go", "package sub\nfunc Y() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	idx := triggerIncremental(t, p, filepath.Join(root, "sub"))
	for _, rel := range []string{"sub/x.go", "sub/y.go"} {
		if _, ok := findNode(idx, NodeFile, rel, filepath.Base(rel)); ok {
			t.Fatalf("%s should be purged with its parent directory: %+v", rel, idx.Nodes())
		}
	}
	if _, ok := findNode(idx, NodeFile, "a.go", "a.go"); !ok {
		t.Fatal("unrelated file should remain indexed")
	}
}

// TestCachedProviderCanceledIncrementalUpdateRequeuesPending guards a real
// gap: applyChanges bailing out on a canceled context must not silently
// lose the paths it hadn't gotten to yet. Without requeuing, drainPending
// already removed them from p.pending, dirty would still get cleared on a
// non-nil-idx return, and those file changes would never be re-applied
// until something else happened to touch the same paths again.
func TestCachedProviderCanceledIncrementalUpdateRequeuesPending(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc A2() {}\n")
	absPath := filepath.Join(root, "a.go")
	p.addPending(absPath)
	p.dirty.Store(true)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Index(canceledCtx); err == nil {
		t.Fatal("Index with a canceled context should return an error, not silently succeed")
	}
	if !p.dirty.Load() {
		t.Fatal("dirty should stay true after a failed rebuild, so a later Index retries")
	}

	idx, err := p.Index(context.Background())
	if err != nil {
		t.Fatalf("retry Index: %v", err)
	}
	found := false
	for _, s := range idx.SymbolsForFile("a.go") {
		if s.Name == "A2" {
			found = true
		}
	}
	if !found {
		t.Fatal("the change should still be applied on retry — it must not have been lost when the first attempt was canceled")
	}
}

// countedCtx cancels itself once Err() has been called more than allowAfter
// times, letting a test deterministically interrupt a multi-step operation
// partway through instead of racing a real deadline.
type countedCtx struct {
	context.Context
	allowAfter int32
	calls      atomic.Int32
}

func (c *countedCtx) Err() error {
	if c.calls.Add(1) > c.allowAfter {
		return context.Canceled
	}
	return nil
}

// TestCachedProviderInterruptedDirectoryWalkRequeuesDirectory covers the
// same class of bug as the canceled-single-file test above, for the
// directory branch specifically: walkSubtree can partially index a
// directory (some files upserted, some not) before a canceled context
// aborts it. That partial result must not be reported as done — the
// directory needs to go back into pending so a retry finishes the walk,
// not just whichever single top-level path was being processed when
// cancellation was noticed.
//
// allowAfter is tuned to the exact number of ctx.Err() calls this scenario
// makes: 1 for applyChanges' own top-of-loop check, then 1 per node
// walkSubtree's callback visits (the "moved" directory itself, then
// moved/x.go) — so cancellation lands after x.go's directory entry is
// visited but before moved/y.go is reached, leaving y.go unindexed by the
// first attempt.
func TestCachedProviderInterruptedDirectoryWalkRequeuesDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	newDir := filepath.Join(root, "moved")
	write(t, root, "moved/x.go", "package moved\nfunc X() {}\n")
	write(t, root, "moved/y.go", "package moved\nfunc Y() {}\n")
	p.addPending(newDir)
	p.dirty.Store(true)

	ctx := &countedCtx{Context: context.Background(), allowAfter: 2}
	if _, err := p.Index(ctx); err == nil {
		t.Fatal("Index should surface the cancellation, not silently succeed with a partially-indexed directory")
	}
	if !p.dirty.Load() {
		t.Fatal("dirty should stay true so a later Index retries")
	}

	idx, err := p.Index(context.Background())
	if err != nil {
		t.Fatalf("retry Index: %v", err)
	}
	for _, rel := range []string{"moved/x.go", "moved/y.go"} {
		if _, ok := findNode(idx, NodeFile, rel, filepath.Base(rel)); !ok {
			t.Fatalf("%s should be indexed once the retry completes the interrupted walk: %+v", rel, idx.Nodes())
		}
	}
}

func TestCachedProviderIncrementalUpdateFallsBackToFullBuildOverCap(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})
	stateBefore := p.state

	paths := make([]string, 0, maxIncrementalChanges+1)
	for i := range maxIncrementalChanges + 1 {
		rel := fmt.Sprintf("extra%d.go", i)
		write(t, root, rel, fmt.Sprintf("package main\nfunc F%d() {}\n", i))
		paths = append(paths, filepath.Join(root, rel))
	}
	idx := triggerIncremental(t, p, paths...)
	if p.state == stateBefore {
		t.Fatal("an oversized changeset should replace the persistent state with a fresh full build")
	}
	if _, ok := findNode(idx, NodeFile, "a.go", "a.go"); !ok {
		t.Fatal("a file from before the fallback should still be present — a full Build re-walks the whole workspace, not just the pending set")
	}
	for i := range maxIncrementalChanges + 1 {
		rel := fmt.Sprintf("extra%d.go", i)
		if _, ok := findNode(idx, NodeFile, rel, rel); !ok {
			t.Fatalf("%s missing after fallback full build: %+v", rel, idx.Nodes())
		}
	}
}

// TestCachedProviderIncrementalUpdateResolvesEdgeBetweenTwoFilesInSameBatch
// covers a debounced batch touching both sides of a new import at once
// (e.g. a multi-file refactor saved together) — edge resolution runs once,
// after every pending path in the batch has been upserted, so processing
// order within the batch must not matter.
func TestCachedProviderIncrementalUpdateResolvesEdgeBetweenTwoFilesInSameBatch(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/app/app.go", "package app\n\nfunc Run() {}\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc old() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	write(t, root, "internal/app/app.go", "package app\n\nimport \"example.com/test/internal/lib\"\n\nfunc Run() { lib.New() }\n")
	write(t, root, "internal/lib/lib.go", "package lib\n\nfunc New() {}\n")
	idx := triggerIncremental(t, p, filepath.Join(root, "internal/app/app.go"), filepath.Join(root, "internal/lib/lib.go"))

	deps := idx.Dependencies("internal/app/app.go", 10)
	if len(deps) != 1 || deps[0].RelPath != "internal/lib/lib.go" {
		t.Fatalf("cross-file edge within one batch = %+v, want internal/lib/lib.go", deps)
	}
}

// TestCachedProviderRepeatedIncrementalUpdatesDoNotDoubleCountStats guards
// the subtlest correctness risk in the incremental design: aggregateStats
// writes into the same persistent nodes map every time finalize runs. If a
// directory's or an untouched file's Stats field were ever read back as the
// aggregation's starting point (instead of the separately-tracked "own"
// stats), repeated finalizes would inflate LOC/Symbols/Files on every call.
func TestCachedProviderRepeatedIncrementalUpdatesDoNotDoubleCountStats(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pkg/a.go", "package pkg\nfunc A() {}\n")
	write(t, root, "pkg/b.go", "package pkg\nfunc B() {}\n")
	p := newIncrementalTestProvider(t, BuildOptions{Root: root})

	root0, _ := findNode(p.snapshot, NodeDirectory, ".", filepath.Base(root))
	pkg0, _ := findNode(p.snapshot, NodeDirectory, "pkg", "pkg")
	a0, _ := findNode(p.snapshot, NodeFile, "pkg/a.go", "a.go")

	// Trigger three separate incremental updates, none of which touch
	// pkg/b.go or the root directory at all — their stats must stay
	// identical across every one of these finalize() calls.
	for i := range 3 {
		write(t, root, "pkg/a.go", fmt.Sprintf("package pkg\nfunc A() {}\n// rev %d\n", i))
		idx := triggerIncremental(t, p, filepath.Join(root, "pkg/a.go"))

		pkgN, ok := findNode(idx, NodeDirectory, "pkg", "pkg")
		if !ok {
			t.Fatalf("round %d: pkg directory missing", i)
		}
		if pkgN.Stats.Files != pkg0.Stats.Files || pkgN.Stats.Symbols != pkg0.Stats.Symbols {
			t.Fatalf("round %d: pkg dir stats = %+v, want unchanged from %+v (double-counting bug)", i, pkgN.Stats, pkg0.Stats)
		}
		rootN, ok := findNode(idx, NodeDirectory, ".", filepath.Base(root))
		if !ok {
			t.Fatalf("round %d: root directory missing", i)
		}
		if rootN.Stats.Files != root0.Stats.Files || rootN.Stats.Symbols != root0.Stats.Symbols {
			t.Fatalf("round %d: root dir stats = %+v, want unchanged from %+v (double-counting bug)", i, rootN.Stats, root0.Stats)
		}
		aN, ok := findNode(idx, NodeFile, "pkg/a.go", "a.go")
		if !ok {
			t.Fatalf("round %d: pkg/a.go missing", i)
		}
		if aN.Stats.Symbols != a0.Stats.Symbols {
			t.Fatalf("round %d: pkg/a.go stats = %+v, want Symbols=%d (double-counting bug)", i, aN.Stats, a0.Stats.Symbols)
		}
	}
}
