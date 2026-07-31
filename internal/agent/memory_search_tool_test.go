package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/memory"
)

func writeTestMemory(t *testing.T, dir, name, memType, desc, body string) {
	t.Helper()
	content := memory.RenderFrontmatter(name, memType, desc, time.Now()) + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMemorySearchTool_FindsRelevantMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	memDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMemory(t, memDir, "table-tests", "feedback",
		"user prefers table-driven tests",
		"Always use table-driven tests in Go. Subtests with t.Run for each case.")
	writeTestMemory(t, memDir, "no-emoji", "feedback",
		"no emoji in UI output",
		"Plain text for picker headers and status lines.")
	writeTestMemory(t, memDir, "deploy-process", "reference",
		"deployment uses ArgoCD",
		"ArgoCD syncs from the main branch to staging.")

	cwd := t.TempDir()
	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)

	tool := &MemorySearchTool{Cwd: cwdRef, Embedder: nil}

	args, _ := json.Marshal(memorySearchArgs{Query: "testing patterns", Scope: "user"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "table-tests") {
		t.Errorf("expected table-tests memory in results; got: %s", result)
	}
}

func TestMemorySearchTool_EmptyStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	cwd := t.TempDir()
	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)

	tool := &MemorySearchTool{Cwd: cwdRef, Embedder: nil}

	args, _ := json.Marshal(memorySearchArgs{Query: "anything"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if result != "no memories found" {
		t.Errorf("expected 'no memories found'; got: %s", result)
	}
}

func TestMemorySearchTool_EmptyQuery(t *testing.T) {
	cwd := t.TempDir()
	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)

	tool := &MemorySearchTool{Cwd: cwdRef}

	args, _ := json.Marshal(memorySearchArgs{Query: ""})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestMemorySearchTool_HonorsConfiguredStrategy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	memDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The query "suite" overlaps this memory ONLY after stemming
	// (suite ↔ suites). The name/description deliberately avoid the bare
	// token "suite" so the legacy keyword scorer finds no exact match.
	writeTestMemory(t, memDir, "full-runs", "feedback",
		"prefer the local run", "Prefer running the full suites locally.")

	cwd := t.TempDir()
	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)

	args, _ := json.Marshal(memorySearchArgs{Query: "suite", Scope: "user"})

	// Legacy keyword strategy: no stemming, so the only memory scores 0
	// and is filtered out as non-matching.
	kw := &MemorySearchTool{Cwd: cwdRef, Strategy: "keyword"}
	kwOut, err := kw.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kwOut, "no matching memories found") {
		t.Errorf("keyword strategy: stem-only query scores 0 and should be filtered; got: %s", kwOut)
	}

	// BM25 strategy: stems suite↔suites, so the same memory matches.
	bm := &MemorySearchTool{Cwd: cwdRef, Strategy: "bm25"}
	bmOut, err := bm.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bmOut, "full-runs") {
		t.Errorf("bm25 strategy should match the memory via stemming; got: %s", bmOut)
	}
}

func TestMemorySearchTool_AllScopeAppliesProjectShadowing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	cwd := t.TempDir()
	userDir := filepath.Join(home, ".yottacode", "memory", "user")
	projDir := filepath.Join(home, ".yottacode", "memory", "projects", filepath.Base(cwd))
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMemory(t, userDir, "same-name", "reference",
		"user scope old routing rule",
		"The portable rule mentions aardvark-user-marker and should be shadowed.")
	writeTestMemory(t, projDir, "same-name", "project",
		"project scope current routing rule",
		"The repo-specific rule mentions aardvark-project-marker and wins here.")

	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)
	tool := &MemorySearchTool{Cwd: cwdRef, Strategy: "bm25"}
	args, _ := json.Marshal(memorySearchArgs{Query: "aardvark", Scope: "all", Limit: 10})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "aardvark-project-marker") {
		t.Fatalf("all-scope search should return project winner; got: %s", result)
	}
	if strings.Contains(result, "aardvark-user-marker") {
		t.Fatalf("all-scope search returned shadowed user twin: %s", result)
	}
}

func TestTruncateRunes_NeverSplitsMultibyte(t *testing.T) {
	// 400 multibyte runes; cap at 300 must yield valid UTF-8 (300 runes + ellipsis).
	s := strings.Repeat("界", 400)
	got := truncateRunes(s, 300)
	if !utf8.ValidString(got) {
		t.Errorf("truncateRunes produced invalid UTF-8")
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n != 300 {
		t.Errorf("kept %d runes, want 300", n)
	}
	// Short strings pass through unchanged.
	if truncateRunes("abc", 300) != "abc" {
		t.Errorf("short string should be unchanged")
	}
}

func TestMemorySearchTool_ScopeFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	userDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMemory(t, userDir, "user-pref", "user",
		"user prefers short responses",
		"Keep answers concise.")

	cwd := t.TempDir()
	projDir := filepath.Join(home, ".yottacode", "memory", "projects", filepath.Base(cwd))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMemory(t, projDir, "proj-fact", "project",
		"project uses PostgreSQL",
		"Database is PostgreSQL 15.")

	cwdRef := &CwdRef{}
	cwdRef.Set(cwd)
	tool := &MemorySearchTool{Cwd: cwdRef}

	// Search user scope only — should find user-pref but not proj-fact
	args, _ := json.Marshal(memorySearchArgs{Query: "responses concise", Scope: "user"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "user-pref") {
		t.Errorf("user scope should find user-pref; got: %s", result)
	}
	if strings.Contains(result, "proj-fact") {
		t.Errorf("user scope should NOT find proj-fact; got: %s", result)
	}
}
