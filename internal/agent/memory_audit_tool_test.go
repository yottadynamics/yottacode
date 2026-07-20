package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/memory"
)

func TestMemoryAuditTool_ReportsIssuesAndScopes(t *testing.T) {
	cwd, ref := memoryAuditToolTestCwd(t)
	seedAuditMemory(t, cwd, "project", "raw-note", "note", "Durable note", "Durable note")
	seedAuditMemory(t, cwd, "user", "good", "user", "Good fact", "The user prefers concrete summaries with tradeoffs.")

	tool := &MemoryAuditTool{Cwd: ref}
	out, err := tool.Execute(context.Background(), `{"scope":"all","limit":10}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"memories: 2 total", "quick-note", "body-echoes-description", "project/raw-note", "action:", "memory_get"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit output missing %q: %s", want, out)
		}
	}

	out, err = tool.Execute(context.Background(), `{"scope":"all","plan":true}`)
	if err != nil {
		t.Fatalf("plan Execute: %v", err)
	}
	for _, want := range []string{"curation plan:", "Promote or delete quick notes", "project/raw-note"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q: %s", want, out)
		}
	}

	out, err = tool.Execute(context.Background(), `{"scope":"all","propose":true}`)
	if err != nil {
		t.Fatalf("propose Execute: %v", err)
	}
	for _, want := range []string{"curation proposals:", "not applied", "project/raw-note", "rewrite-needs-context"} {
		if !strings.Contains(out, want) {
			t.Errorf("proposal output missing %q: %s", want, out)
		}
	}

	out, err = tool.Execute(context.Background(), `{"scope":"all","summary":true}`)
	if err != nil {
		t.Fatalf("summary Execute: %v", err)
	}
	for _, want := range []string{"memory health: 2 memories", "quick notes: 1", "vague bodies: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output missing %q: %s", want, out)
		}
	}

	out, err = tool.Execute(context.Background(), `{"scope":"user"}`)
	if err != nil {
		t.Fatalf("user Execute: %v", err)
	}
	if strings.Contains(out, "raw-note") {
		t.Errorf("user-scoped audit included project note: %s", out)
	}
	if !strings.Contains(out, "memory store looks curated") {
		t.Errorf("user-scoped audit should be clean: %s", out)
	}
}

func TestMemoryAuditTool_InvalidScope(t *testing.T) {
	_, ref := memoryAuditToolTestCwd(t)
	tool := &MemoryAuditTool{Cwd: ref}
	if _, err := tool.Execute(context.Background(), `{"scope":"org"}`); err == nil {
		t.Fatal("expected invalid scope error")
	}
}

func memoryAuditToolTestCwd(t *testing.T) (string, *CwdRef) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	return cwd, NewCwdRef(cwd)
}

func seedAuditMemory(t *testing.T, cwd, scope, name, typ, desc, body string) {
	t.Helper()
	path, err := memory.MemoryFilePath(scope, name, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := memory.RenderFrontmatter(name, typ, desc, time.Now()) + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
