package codemap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/lsp"
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

// TestCachedProviderRealWatchRebuildsWithNewContent closes the gap between the
// deterministic dirty-flag tests below and the debounce-timing test (which only
// checks a new snapshot appears): this goes through a real StartWatch and
// fsnotify event, and checks the resulting index's actual content.
func TestCachedProviderRealWatchRebuildsWithNewContent(t *testing.T) {
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

// newWatchedTestProvider simulates a running watch deterministically, without
// real fsnotify or debounce timing: with watchStarted set, Index serves the
// cached snapshot until a test marks the provider dirty.
func newWatchedTestProvider(t *testing.T, opts BuildOptions) *CachedProvider {
	t.Helper()
	p := &CachedProvider{Options: opts}
	p.watchStarted.Store(true)
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatalf("initial Index: %v", err)
	}
	return p
}

func hasFile(idx *CodeIndex, rel string) bool {
	_, ok := idx.ResolveFile(rel)
	return ok
}

func TestCachedProviderServesCachedSnapshotUntilDirty(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	source := &countingSource{}
	p := newWatchedTestProvider(t, BuildOptions{Root: root, Source: source})
	callsAfterBuild := source.calls

	for range 3 {
		if _, err := p.Index(context.Background()); err != nil {
			t.Fatalf("Index: %v", err)
		}
	}
	if source.calls != callsAfterBuild {
		t.Fatalf("clean Index re-parsed files: %d calls, want %d", source.calls, callsAfterBuild)
	}

	p.dirty.Store(true)
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatalf("Index after dirty: %v", err)
	}
	if source.calls == callsAfterBuild {
		t.Fatal("dirty Index did not rebuild")
	}
	if p.dirty.Load() {
		t.Fatal("a successful rebuild must clear the dirty flag")
	}
}

func TestCachedProviderDirtyRebuildReflectsAddsEditsAndDeletes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	write(t, root, "gone.go", "package main\nfunc Gone() {}\n")
	p := newWatchedTestProvider(t, BuildOptions{Root: root})

	write(t, root, "a.go", "package main\nfunc A() {}\nfunc Edited() {}\n")
	write(t, root, "sub/new.go", "package sub\nfunc New() {}\n")
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	p.dirty.Store(true)
	idx, err := p.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !hasFile(idx, "sub/new.go") {
		t.Error("new file in a new directory is missing")
	}
	if hasFile(idx, "gone.go") {
		t.Error("deleted file is still indexed")
	}
	found := false
	for _, s := range idx.SymbolsForFile("a.go") {
		found = found || s.Name == "Edited"
	}
	if !found {
		t.Error("edited symbol is missing")
	}
}

// markDirtySource marks the provider dirty from inside a build, as a file
// change landing mid-build would.
type markDirtySource struct {
	p     *CachedProvider
	fired bool
}

func (s *markDirtySource) Symbols(context.Context, string) ([]lsp.Symbol, string, error) {
	if !s.fired && s.p != nil {
		s.fired = true
		s.p.dirty.Store(true)
	}
	return nil, "test", nil
}

// TestCachedProviderChangeDuringBuildIsNotLost guards the ordering of the
// dirty flag: it must be cleared before the build starts, or an event that
// arrives mid-build is wiped and the stale snapshot is served indefinitely.
func TestCachedProviderChangeDuringBuildIsNotLost(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	src := &markDirtySource{}
	p := &CachedProvider{Options: BuildOptions{Root: root, Source: src}}
	src.p = p
	p.watchStarted.Store(true)
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !p.dirty.Load() {
		t.Fatal("a change that arrived mid-build was lost; the next Index would serve a stale snapshot")
	}
}

func TestCachedProviderFailedRebuildStaysDirty(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	p := newWatchedTestProvider(t, BuildOptions{Root: root})
	p.dirty.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Index(ctx); err == nil {
		t.Fatal("Index with a canceled context should fail")
	}
	if !p.dirty.Load() {
		t.Fatal("a failed rebuild must leave the provider dirty so the next call retries")
	}
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatalf("retry Index: %v", err)
	}
}

// timeoutOnceSource stalls on its first call so the build blows its time budget,
// then runs at full speed, like a machine that was briefly overloaded.
type timeoutOnceSource struct {
	slowFirst time.Duration
	calls     int
}

func (s *timeoutOnceSource) Symbols(context.Context, string) ([]lsp.Symbol, string, error) {
	s.calls++
	if s.calls == 1 {
		time.Sleep(s.slowFirst)
	}
	return nil, "test", nil
}

func TestCachedProviderRetriesTimedOutSnapshotAfterExpiry(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		write(t, root, fmt.Sprintf("f%d.go", i), "package main\n")
	}
	src := &timeoutOnceSource{slowFirst: 120 * time.Millisecond}
	p := &CachedProvider{Options: BuildOptions{Root: root, Source: src, MaxDuration: 50 * time.Millisecond}, RetryTimedOutAfter: 150 * time.Millisecond}
	ctx := context.Background()

	first, err := p.Index(ctx)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !first.Info().TimedOut {
		t.Fatalf("first build should have timed out: %s", first.Info())
	}
	// Inside the retry window the partial snapshot is served as is.
	if again, _ := p.Index(ctx); again != first {
		t.Fatal("a timed-out snapshot must be reused inside the retry window")
	}
	time.Sleep(160 * time.Millisecond)
	retried, err := p.Index(ctx)
	if err != nil {
		t.Fatalf("Index after expiry: %v", err)
	}
	if retried == first || retried.Info().TimedOut || retried.Info().Files != 5 {
		t.Fatalf("expected a complete rebuild after the window, got %s", retried.Info())
	}
	// A complete snapshot is cached for good.
	time.Sleep(160 * time.Millisecond)
	if stable, _ := p.Index(ctx); stable != retried {
		t.Fatal("a complete snapshot must not be rebuilt on a timer")
	}
}

func TestCachedProviderDoesNotRetryFileCapSnapshot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\n")
	write(t, root, "b.go", "package main\n")
	p := &CachedProvider{Options: BuildOptions{Root: root, MaxFiles: 1}, RetryTimedOutAfter: time.Millisecond}
	first, err := p.Index(context.Background())
	if err != nil || !first.Info().Capped {
		t.Fatalf("setup: %v %s", err, first.Info())
	}
	time.Sleep(10 * time.Millisecond)
	if again, _ := p.Index(context.Background()); again != first {
		t.Fatal("a file-cap snapshot would hit the same cap again, so it must not be retried")
	}
}
