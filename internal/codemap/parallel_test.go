package codemap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// mixedFixture writes a small multi-language tree with cross-file imports, so
// every per-language resolver has something to resolve.
func mixedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/m\n")
	for i := 0; i < 12; i++ {
		write(t, root, fmt.Sprintf("pkg%02d/p.go", i), fmt.Sprintf("package pkg%02d\n\nimport \"example.com/m/pkg%02d\"\n\nfunc F%d() { pkg%02d.F%d() }\n", i, (i+1)%12, i, (i+1)%12, (i+1)%12))
		write(t, root, fmt.Sprintf("pkg%02d/p_test.go", i), fmt.Sprintf("package pkg%02d\n\nimport \"testing\"\n\nfunc TestF%d(t *testing.T) {}\n", i, i))
	}
	write(t, root, "web/tsconfig.json", `{"compilerOptions":{"baseUrl":"src","paths":{"@/*":["*"]}}}`)
	for i := 0; i < 10; i++ {
		write(t, root, fmt.Sprintf("web/src/m%d.ts", i), fmt.Sprintf("import { x } from '@/m%d'\nexport const y%d = 1\n", (i+1)%10, i))
	}
	write(t, root, "src/app/__init__.py", "")
	for i := 0; i < 10; i++ {
		write(t, root, fmt.Sprintf("src/app/mod%d.py", i), fmt.Sprintf("from app import mod%d\n\ndef f%d():\n    pass\n", (i+1)%10, i))
	}
	for i := 0; i < 5; i++ {
		write(t, root, fmt.Sprintf("rs/src/m%d.rs", i), "pub fn f() {}\n")
	}
	write(t, root, "docs/readme.md", "mentions pkg00 and mod3\n")
	return root
}

// TestParallelBuildMatchesSequential: how many workers parse must never change
// the index. Compared on a mixed fixture and on this repository's own tree.
func TestParallelBuildMatchesSequential(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"mixed fixture": mixedFixture(t), "this repository": repo} {
		var versions []string
		var edges []int
		for _, workers := range []int{1, 2, 8, 0} {
			idx, err := Build(context.Background(), BuildOptions{Root: root, MaxFiles: 100000, Workers: workers})
			if err != nil {
				t.Fatalf("%s workers=%d: %v", name, workers, err)
			}
			versions = append(versions, idx.Info().Version)
			edges = append(edges, len(idx.Edges(EdgeImports)))
		}
		for i := 1; i < len(versions); i++ {
			if versions[i] != versions[0] || edges[i] != edges[0] {
				t.Fatalf("%s: index differs by worker count: versions %v, edge counts %v", name, versions, edges)
			}
		}
		if edges[0] == 0 {
			t.Fatalf("%s: fixture produced no edges, so the comparison proves nothing", name)
		}
	}
}

func TestParallelBuildHonorsFileCapExactly(t *testing.T) {
	root := mixedFixture(t)
	for _, workers := range []int{1, 8} {
		idx, err := Build(context.Background(), BuildOptions{Root: root, MaxFiles: 7, Workers: workers})
		if err != nil {
			t.Fatal(err)
		}
		if info := idx.Info(); info.Files != 7 || !info.Capped {
			t.Fatalf("workers=%d: %s, want exactly 7 files and Capped", workers, info)
		}
	}
}

func TestParallelBuildStopsAtTimeBudget(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		write(t, root, fmt.Sprintf("f%02d.go", i), "package main\n")
	}
	// 60 files x 20ms across 4 workers is ~300ms of work against a 60ms budget.
	idx, err := Build(context.Background(), BuildOptions{Root: root, MaxDuration: 60 * time.Millisecond, Workers: 4, Source: slowSource{per: 20 * time.Millisecond}})
	if err != nil {
		t.Fatalf("a timed-out build returns a partial index, not an error: %v", err)
	}
	info := idx.Info()
	if !info.TimedOut || info.Files == 0 || info.Files >= 60 {
		t.Fatalf("want a partial index that timed out, got %s", info)
	}
}

func TestParallelBuildCancelsPromptly(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		write(t, root, fmt.Sprintf("f%02d.go", i), "package main\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Build(ctx, BuildOptions{Root: root, Workers: 4, Source: slowSource{per: 50 * time.Millisecond}})
	if err == nil {
		t.Fatal("a canceled build must return the context error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %s; workers should stop after their current file", elapsed)
	}
}

// failingSource errors for one file, as a language server can.
type failingSource struct{ bad string }

func (s failingSource) Symbols(_ context.Context, path string) ([]lsp.Symbol, string, error) {
	if strings.HasSuffix(path, s.bad) {
		return nil, "", fmt.Errorf("boom")
	}
	return []lsp.Symbol{{Name: "Sym", Kind: "function"}}, "test", nil
}

// A symbol failure keeps the file (and its imports) but yields no symbols, the
// same as before builds were parallel.
func TestSymbolSourceFailureKeepsFileWithoutSymbols(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ok.go", "package main\n")
	write(t, root, "bad.go", "package main\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root, Source: failingSource{bad: "bad.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.ResolveFile("bad.go"); !ok {
		t.Fatal("bad.go should still be indexed")
	}
	if n := len(idx.SymbolsForFile("bad.go")); n != 0 {
		t.Fatalf("bad.go has %d symbols, want 0", n)
	}
	if n := len(idx.SymbolsForFile("ok.go")); n != 1 {
		t.Fatalf("ok.go has %d symbols, want 1", n)
	}
}

// concurrentCounter is a Source that counts calls and can stall each one.
type concurrentCounter struct {
	n     atomic.Int64
	delay time.Duration
}

func (s *concurrentCounter) Symbols(ctx context.Context, _ string) ([]lsp.Symbol, string, error) {
	s.n.Add(1)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	return nil, "test", nil
}

func makeOld(t *testing.T, root, rel string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(root, rel), when, when); err != nil {
		t.Fatal(err)
	}
}

// panickingSource blows up on one file, as a buggy parser can on malformed input.
type panickingSource struct{ bad string }

func (s panickingSource) Symbols(_ context.Context, path string) ([]lsp.Symbol, string, error) {
	if strings.HasSuffix(path, s.bad) {
		panic("parser bug")
	}
	return []lsp.Symbol{{Name: "Sym", Kind: "function"}}, "test", nil
}

func TestBuildSurvivesAPanickingParserAndSaysSo(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, root, fmt.Sprintf("ok%02d.go", i), "package main\n")
	}
	write(t, root, "evil.go", "package main\n")
	for _, workers := range []int{1, 8} {
		idx, err := Build(context.Background(), BuildOptions{Root: root, Workers: workers, Source: panickingSource{bad: "evil.go"}})
		if err != nil {
			t.Fatalf("workers=%d: a panic in one file's parser must not fail the build: %v", workers, err)
		}
		info := idx.Info()
		if info.Files != 21 || info.ParseFailures != 1 || !strings.Contains(info.String(), "1 files failed to parse") {
			t.Fatalf("workers=%d: %s", workers, info)
		}
		if n := len(idx.SymbolsForFile("evil.go")); n != 0 {
			t.Fatalf("the failed file has %d symbols, want 0", n)
		}
		if n := len(idx.SymbolsForFile("ok00.go")); n != 1 {
			t.Fatalf("healthy files must be unaffected, ok00.go has %d symbols", n)
		}
	}
}

func TestBuildSkipsOversizedFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "small.go", "package main\n")
	write(t, root, "huge.go", "package main\n// "+strings.Repeat("x", maxFileBytes)+"\n")
	write(t, root, "real.py", "def f():\n    pass\n")
	outside := t.TempDir()
	write(t, outside, "secret.py", "def leaked():\n    pass\n")
	if err := os.Symlink(filepath.Join(outside, "secret.py"), filepath.Join(root, "link.py")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.ResolveFile("huge.go"); ok {
		t.Error("a file over the size limit must not be indexed")
	}
	if _, ok := idx.ResolveFile("link.py"); ok {
		t.Error("a symlinked file must not be indexed: it can point outside the repository")
	}
	for _, want := range []string{"small.go", "real.py"} {
		if _, ok := idx.ResolveFile(want); !ok {
			t.Errorf("%s should be indexed", want)
		}
	}
	info := idx.Info()
	if info.Oversized != 1 || !strings.Contains(info.String(), "1 files over 2 MiB skipped") {
		t.Errorf("oversized file not reported: %s", info)
	}
	for _, n := range idx.Nodes() {
		if n.Name == "leaked" {
			t.Error("a symbol from the symlink target leaked into the index")
		}
	}
}
