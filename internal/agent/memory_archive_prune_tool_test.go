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

func TestMemoryArchivePruneTool_DryRunDefaultIsReadOnly(t *testing.T) {
	cwd, ref, archivePath := seedArchivePruneToolArchive(t)
	_ = cwd
	tool := &MemoryArchivePruneTool{Cwd: ref}
	if tool.RequiresApproval(`{"scope":"user","keep_latest":1}`) {
		t.Fatal("omitted dry_run should default to true/read-only")
	}
	out, err := tool.Execute(context.Background(), `{"scope":"user","keep_latest":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "would delete 1 archive") {
		t.Fatalf("unexpected dry-run output: %s", out)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("dry run should not delete archive: %v", err)
	}
}

func TestMemoryArchivePruneTool_DeleteRequiresApprovalAndDeletes(t *testing.T) {
	_, ref, archivePath := seedArchivePruneToolArchive(t)
	tool := &MemoryArchivePruneTool{Cwd: ref}
	if !tool.RequiresApproval(`{"scope":"user","keep_latest":1,"dry_run":false}`) {
		t.Fatal("dry_run=false should require approval")
	}
	out, err := tool.Execute(context.Background(), `{"scope":"user","keep_latest":1,"dry_run":false}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "deleted 1 archive") {
		t.Fatalf("unexpected delete output: %s", out)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive should be deleted, stat err = %v", err)
	}
}

func seedArchivePruneToolArchive(t *testing.T) (string, *CwdRef, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	memPath, err := memory.MemoryFilePath("user", "prefs", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(memPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(memory.RenderFrontmatter("prefs", "user", "Prefs", time.Now())+"v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath, err := memory.ArchivePrior(memPath, "100")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(memory.RenderFrontmatter("prefs", "user", "Prefs", time.Now())+"v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ArchivePrior(memPath, "200"); err != nil {
		t.Fatal(err)
	}
	return cwd, NewCwdRef(cwd), archivePath
}
