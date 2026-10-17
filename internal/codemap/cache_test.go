package codemap

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testRacyWindow is the trust window tests use in place of the production two
// seconds. Tests can backdate a file's modification time but not its inode change
// time, so the real window would refuse to cache anything they create. It must stay
// well above the filesystem's timestamp tick (several milliseconds on Linux, where
// coarse clocks stamp files), which is exactly the hazard the real window covers.
const testRacyWindow = 25 * time.Millisecond

func newTestCache() *ParseCache { return &ParseCache{racy: testRacyWindow} }

func oldTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		write(t, root, rel, body)
		makeOld(t, root, rel, time.Hour)
	}
	time.Sleep(2 * testRacyWindow) // let the inode change times age past the test window
	return root
}

func TestParseCacheSkipsUnchangedFilesOnRebuild(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\nfunc A() {}\n", "b.go": "package main\nfunc B() {}\n", "c.py": "def c():\n    pass\n"})
	src := &countingSource{}
	cache := newTestCache()
	opts := BuildOptions{Root: root, Source: src, Cache: cache}

	first, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if src.calls() != 3 || cache.Len() != 3 {
		t.Fatalf("cold build: %d source calls, %d cached; want 3 and 3", src.calls(), cache.Len())
	}
	second, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if src.calls() != 3 {
		t.Fatalf("warm rebuild re-parsed %d files, want 0", src.calls()-3)
	}
	if first.Info().Version != second.Info().Version {
		t.Fatalf("cached rebuild changed the index: %s vs %s", first.Info(), second.Info())
	}
}

func TestParseCacheReparsesOnlyChangedFile(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\nfunc A() {}\n", "b.go": "package main\nfunc B() {}\n"})
	src := &countingSource{}
	opts := BuildOptions{Root: root, Source: src, Cache: newTestCache()}
	if _, err := Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	// A different length changes the key even when the timestamp is restored.
	write(t, root, "a.go", "package main\nfunc A() {}\nfunc Added() {}\n")
	makeOld(t, root, "a.go", 30*time.Minute)
	idx, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if src.calls() != 3 {
		t.Fatalf("source calls = %d, want 2 (cold) + 1 (changed file)", src.calls())
	}
	_ = idx
}

// TestParseCacheNeverServesStaleContent: after an edit the new content must win
// even though the cache already holds the file.
func TestParseCacheNeverServesStaleContent(t *testing.T) {
	root := oldTree(t, map[string]string{"go.mod": "module example.com/m\n", "a.go": "package main\n\nfunc OldName() {}\n"})
	cache := newTestCache()
	opts := BuildOptions{Root: root, Cache: cache}
	if _, err := Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.go", "package main\n\nfunc NewName() {}\n") // mtime is now
	idx, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range idx.SymbolsForFile("a.go") {
		names = append(names, s.Name)
	}
	if len(names) != 1 || names[0] != "NewName" {
		t.Fatalf("symbols after edit = %v, want [NewName]", names)
	}
}

// TestParseCacheDetectsRewriteThatRestoresTimestamp: same size, original mtime put
// back (tar, rsync -t, touch -r). Only the inode change time exposes it.
func TestParseCacheDetectsRewriteThatRestoresTimestamp(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\n\nfunc AAAA() {}\n"})
	fi, err := os.Stat(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if changeTime(fi) == 0 {
		t.Skip("inode change time is not available on this platform")
	}
	cache := newTestCache()
	opts := BuildOptions{Root: root, Cache: cache}
	if _, err := Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.go", "package main\n\nfunc BBBB() {}\n") // same length
	if err := os.Chtimes(filepath.Join(root, "a.go"), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	idx, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	syms := idx.SymbolsForFile("a.go")
	if len(syms) != 1 || syms[0].Name != "BBBB" {
		t.Fatalf("stale parse served after a timestamp-preserving rewrite: %v", syms)
	}
}

func TestParseCacheSkipsRecentlyModifiedFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "fresh.go", "package main\nfunc F() {}\n") // modified just now
	src := &countingSource{}
	cache := &ParseCache{} // the real two-second window
	opts := BuildOptions{Root: root, Source: src, Cache: cache}
	for i := 0; i < 2; i++ {
		if _, err := Build(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	if cache.Len() != 0 || src.calls() != 2 {
		t.Fatalf("a file modified within the racy window must not be cached: len=%d calls=%d", cache.Len(), src.calls())
	}
}

func TestParseCachePrunesDeletedFilesButNotOnPartialBuilds(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\n", "b.go": "package main\n", "c.go": "package main\n"})
	cache := newTestCache()
	build := func(max int) {
		if _, err := Build(context.Background(), BuildOptions{Root: root, MaxFiles: max, Cache: cache}); err != nil {
			t.Fatal(err)
		}
	}
	build(0)
	if cache.Len() != 3 {
		t.Fatalf("len = %d, want 3", cache.Len())
	}
	build(1) // capped: saw one file, must not evict the others
	if cache.Len() != 3 {
		t.Fatalf("a partial build evicted entries: len = %d, want 3", cache.Len())
	}
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	build(0)
	if cache.Len() != 2 {
		t.Fatalf("deleted file not pruned after a complete build: len = %d, want 2", cache.Len())
	}
}

func TestProviderRebuildUsesParseCache(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\nfunc A() {}\n", "b.go": "package main\nfunc B() {}\n", "c.go": "package main\nfunc C() {}\n"})
	src := &countingSource{}
	p := newWatchedTestProvider(t, BuildOptions{Root: root, Source: src})
	if src.calls() != 3 {
		t.Fatalf("cold build calls = %d, want 3", src.calls())
	}
	write(t, root, "b.go", "package main\nfunc B() {}\nfunc B2() {}\n")
	p.dirty.Store(true)
	if _, err := p.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.calls() != 4 {
		t.Fatalf("rebuild after one edit made %d source calls, want 1", src.calls()-3)
	}
}

func TestParseCacheIsSafeForConcurrentBuilds(t *testing.T) {
	root := oldTree(t, map[string]string{"a.go": "package main\n", "b.go": "package main\n", "c.go": "package main\n", "d.go": "package main\n"})
	cache := newTestCache()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Build(context.Background(), BuildOptions{Root: root, Cache: cache}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
