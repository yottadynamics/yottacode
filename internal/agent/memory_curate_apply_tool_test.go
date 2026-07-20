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

func TestMemoryCurateApplyTool_DeleteEmptyBody(t *testing.T) {
	cwd, ref := curateApplyTestCwd(t)
	seedCurateApplyMemory(t, cwd, "project", "empty", "project", "Empty fact", "")

	tool := &MemoryCurateApplyTool{Cwd: ref}
	out, err := tool.Execute(context.Background(), `{"problem":"empty-body","scope":"project","name":"empty"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, `deleted empty project memory "empty"`) {
		t.Fatalf("unexpected output: %s", out)
	}
	path, err := memory.MemoryFilePath("project", "empty", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("memory file should be removed, stat err = %v", err)
	}
}

func TestMemoryCurateApplyTool_RefusesNonEmptyDelete(t *testing.T) {
	cwd, ref := curateApplyTestCwd(t)
	seedCurateApplyMemory(t, cwd, "project", "non-empty", "project", "Fact", "body")

	tool := &MemoryCurateApplyTool{Cwd: ref}
	if _, err := tool.Execute(context.Background(), `{"problem":"empty-body","scope":"project","name":"non-empty"}`); err == nil {
		t.Fatal("expected non-empty memory to be rejected")
	}
}

func TestMemoryCurateApplyTool_MovePortableProjectMemory(t *testing.T) {
	cwd, ref := curateApplyTestCwd(t)
	seedCurateApplyMemory(t, cwd, "project", "portable", "feedback", "User preference", "The user prefers concise answers.")

	tool := &MemoryCurateApplyTool{Cwd: ref}
	out, err := tool.Execute(context.Background(), `{"problem":"portable-in-project","scope":"project","name":"portable"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, `moved project memory "portable" to user scope`) {
		t.Fatalf("unexpected output: %s", out)
	}
	projectPath, err := memory.MemoryFilePath("project", "portable", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project memory should be removed, stat err = %v", err)
	}
	projectDir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, memory.HistoryDirName, "portable.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("move history should be written under destination user scope only, stat err = %v", err)
	}
	userPath, err := memory.MemoryFilePath("user", "portable", cwd)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read moved memory: %v", err)
	}
	if !strings.Contains(string(data), "The user prefers concise answers.") {
		t.Fatalf("moved memory lost body: %s", data)
	}
	userDir, err := memory.UserMemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(filepath.Join(userDir, memory.HistoryDirName, "portable.jsonl"))
	if err != nil {
		t.Fatalf("read move history: %v", err)
	}
	if !strings.Contains(string(history), "move-portable") || !strings.Contains(string(history), "portable-in-project") {
		t.Fatalf("move history missing action/reason: %s", history)
	}
}

func TestMemoryCurateApplyTool_RefusesMoveWhenUserTargetExists(t *testing.T) {
	cwd, ref := curateApplyTestCwd(t)
	seedCurateApplyMemory(t, cwd, "project", "portable", "feedback", "User preference", "project body")
	seedCurateApplyMemory(t, cwd, "user", "portable", "feedback", "User preference", "user body")

	tool := &MemoryCurateApplyTool{Cwd: ref}
	if _, err := tool.Execute(context.Background(), `{"problem":"portable-in-project","scope":"project","name":"portable"}`); err == nil {
		t.Fatal("expected existing user target to require manual merge")
	}
}

func TestMemoryCurateApplyTool_RequiresApproval(t *testing.T) {
	tool := &MemoryCurateApplyTool{}
	if !tool.RequiresApproval("") {
		t.Fatal("memory_curate_apply must require approval")
	}
	if tool.ParallelSafe("") {
		t.Fatal("memory_curate_apply should not be parallel-safe")
	}
}

func curateApplyTestCwd(t *testing.T) (string, *CwdRef) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	return cwd, NewCwdRef(cwd)
}

func seedCurateApplyMemory(t *testing.T, cwd, scope, name, typ, desc, body string) {
	t.Helper()
	path, err := memory.MemoryFilePath(scope, name, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := memory.RenderFrontmatter(name, typ, desc, time.Now()) + body
	if body != "" {
		contents += "\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := memory.RegenerateMemoryIndex(scope, cwd); err != nil {
		t.Fatal(err)
	}
}
