package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDir_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, tmp, "a.txt", "hi")
	writeFile(t, tmp, "b.txt", "hi")

	tool := &ListDirTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"."}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "f\ta.txt") {
		t.Errorf("missing file marker for a.txt: %q", out)
	}
	if !strings.Contains(out, "f\tb.txt") {
		t.Errorf("missing file marker for b.txt: %q", out)
	}
	if !strings.Contains(out, "d\tsub") {
		t.Errorf("missing dir marker for sub: %q", out)
	}
}

func TestListDir_RespectsMaxEntries(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, tmp, string(rune('a'+i))+".txt", "x")
	}
	tool := &ListDirTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":".","max_entries":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker: %q", out)
	}
}

func TestListDir_MissingPath(t *testing.T) {
	tool := &ListDirTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `{"path":"nope"}`); err == nil {
		t.Errorf("expected error for missing dir")
	}
}

func TestGlob_DoublestarRecurse(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "a", "b"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, tmp, "a/x.go", "x")
	writeFile(t, tmp, "a/b/y.go", "y")
	writeFile(t, tmp, "a/skip.txt", "skip")

	tool := &GlobTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a/x.go") || !strings.Contains(out, "a/b/y.go") {
		t.Errorf("missing matches: %q", out)
	}
	if strings.Contains(out, "skip.txt") {
		t.Errorf("non-matching file leaked: %q", out)
	}
}

func TestGlob_NoMatches(t *testing.T) {
	tmp := t.TempDir()
	tool := &GlobTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"*.nonexistent"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("expected 'no matches', got %q", out)
	}
}

func TestGlob_RequiresPattern(t *testing.T) {
	tool := &GlobTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error on empty pattern")
	}
}

func TestGrep_FindsLiteralMatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.txt", "needle in haystack\nanother needle here\nno match here\n")

	tool := &GrepTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"needle","path":"."}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "needle in haystack") {
		t.Errorf("missing first match: %q", out)
	}
	if !strings.Contains(out, "another needle here") {
		t.Errorf("missing second match: %q", out)
	}
	if !strings.Contains(out, ":1:") || !strings.Contains(out, ":2:") {
		t.Errorf("missing line numbers: %q", out)
	}
}

func TestGrep_IgnoreCase(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "Needle\nnoodle\n")
	tool := &GrepTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"needle","path":".","ignore_case":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Needle") {
		t.Errorf("ignore_case missed 'Needle': %q", out)
	}
}

func TestGrep_NoMatches(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "hello\n")
	tool := &GrepTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"absent","path":"."}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("expected 'no matches', got %q", out)
	}
}

func TestGrep_RegexMode(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "v1.2.3\nv4.5.6\nplain\n")
	tool := &GrepTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"pattern":"v[0-9]+\\.[0-9]+","path":".","regex":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "v1.2.3") || !strings.Contains(out, "v4.5.6") {
		t.Errorf("regex matches missing: %q", out)
	}
	if strings.Contains(out, "plain") {
		t.Errorf("non-matching line leaked: %q", out)
	}
}

func TestGrep_RequiresPattern(t *testing.T) {
	tool := &GrepTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error on missing pattern")
	}
}

func TestListProjectStructure_TreeWithSizesAndMtimes(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "src", "deep"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, tmp, "src/a.go", "package a")
	writeFile(t, tmp, "src/deep/b.go", "package b")
	writeFile(t, tmp, "README.md", "# hi")

	tool := &ListProjectStructureTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"README.md", "src", "src/a.go", "src/deep/b.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	// Sizes should be tab-separated; "package a" is 9 bytes.
	if !strings.Contains(out, "\t9\t") {
		t.Errorf("expected size column to include 9-byte file: %q", out)
	}
}

func TestListProjectStructure_SkipsHeavyDirs(t *testing.T) {
	tmp := t.TempDir()
	for _, dir := range []string{".git", "node_modules", "vendor"} {
		if err := os.MkdirAll(filepath.Join(tmp, dir), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		writeFile(t, tmp, filepath.Join(dir, "junk.txt"), "x")
	}
	writeFile(t, tmp, "main.go", "package main")

	tool := &ListProjectStructureTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("expected main.go in output: %q", out)
	}
	for _, banned := range []string{".git", "node_modules", "vendor", "junk.txt"} {
		if strings.Contains(out, banned) {
			t.Errorf("expected %q to be skipped: %q", banned, out)
		}
	}
}

func TestListProjectStructure_RespectsMaxDepth(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "a", "b", "c"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeFile(t, tmp, "a/x", "1")
	writeFile(t, tmp, "a/b/y", "2")
	writeFile(t, tmp, "a/b/c/z", "3")

	tool := &ListProjectStructureTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"max_depth":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a/x") {
		t.Errorf("depth=2 should reach a/x: %q", out)
	}
	if strings.Contains(out, "a/b/y") {
		t.Errorf("depth=2 should NOT reach a/b/y: %q", out)
	}
}

func TestListProjectStructure_HiddenOptIn(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, ".env", "secret")
	writeFile(t, tmp, "main.go", "main")

	tool := &ListProjectStructureTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, ".env") {
		t.Errorf("hidden file should be skipped by default: %q", out)
	}
	out, err = tool.Execute(context.Background(), `{"include_hidden":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, ".env") {
		t.Errorf("include_hidden should surface .env: %q", out)
	}
}

func TestListProjectStructure_TruncationMarker(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 12; i++ {
		writeFile(t, tmp, fmt.Sprintf("f%02d", i), "x")
	}
	tool := &ListProjectStructureTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"max_entries":3}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated at 3") {
		t.Errorf("expected truncation marker: %q", out)
	}
}

func TestListProjectStructure_RejectsNonDir(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "x")
	tool := &ListProjectStructureTool{Cwd: tmp}
	if _, err := tool.Execute(context.Background(), `{"path":"main.go"}`); err == nil {
		t.Errorf("expected error when path is a file")
	}
}
