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

func TestMemoryGetTool_ReturnsFullUntruncatedBody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memDir := filepath.Join(home, ".yottacode", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A body well over memory_search's 300-char preview cap.
	longBody := strings.Repeat("durable preference line. ", 40)
	content := memory.RenderFrontmatter("go-style", "feedback", "go coding style", time.Now()) + longBody + "\n"
	if err := os.WriteFile(filepath.Join(memDir, "go-style.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cwdRef := &CwdRef{}
	cwdRef.Set(t.TempDir())
	tool := &MemoryGetTool{Cwd: cwdRef}

	args, _ := json.Marshal(memoryGetArgs{Scope: "user", Name: "go-style"})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, longBody) {
		t.Errorf("memory_get must return the full untruncated body; got %d chars", len(out))
	}
	if !strings.Contains(out, "name: go-style") {
		t.Errorf("memory_get should include frontmatter")
	}
}

func TestMemoryGetTool_MissingErrorsCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwdRef := &CwdRef{}
	cwdRef.Set(t.TempDir())
	tool := &MemoryGetTool{Cwd: cwdRef}

	args, _ := json.Marshal(memoryGetArgs{Scope: "user", Name: "does-not-exist"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Error("expected an error for a missing memory")
	}
}
