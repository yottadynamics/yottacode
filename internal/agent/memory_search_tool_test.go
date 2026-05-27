package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

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

	memDir := filepath.Join(home, ".yottacode", "memory")
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

func TestMemorySearchTool_ScopeFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	userDir := filepath.Join(home, ".yottacode", "memory")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestMemory(t, userDir, "user-pref", "user",
		"user prefers short responses",
		"Keep answers concise.")

	cwd := t.TempDir()
	projDir := filepath.Join(home, ".yottacode", "projects", filepath.Base(cwd), "memory")
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
