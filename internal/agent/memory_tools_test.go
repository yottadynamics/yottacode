package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memTestSetup hijacks HOME so the tools write under a tempdir and
// returns (home, cwd). The cwd is also a fresh tempdir so the
// project slug (filepath.Base) is deterministic per test.
func memTestSetup(t *testing.T) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()
	t.Setenv("HOME", home)
	return home, cwd
}

func TestMemorySave_WritesFileAndIndex(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	out, err := tool.Execute(context.Background(), `{
		"scope": "user",
		"type": "feedback",
		"name": "verbose-output",
		"description": "user dislikes wall-of-stack output",
		"content": "Keep stack traces collapsed by default; expand only on request."
	}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(out, "saved user memory") {
		t.Errorf("expected confirmation, got %q", out)
	}
	path := filepath.Join(home, ".yottacode", "memory", "verbose-output.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("memory file missing: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"---",
		"name: verbose-output",
		"type: feedback",
		"description: user dislikes wall-of-stack output",
		"created: ",
		"Keep stack traces collapsed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("memory file missing %q\n--- file ---\n%s", want, body)
		}
	}
	indexPath := filepath.Join(home, ".yottacode", "memory", "MEMORY.md")
	idx, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("index missing: %v", err)
	}
	if !strings.Contains(string(idx), "verbose-output") {
		t.Errorf("index does not list new memory:\n%s", idx)
	}
}

func TestMemorySave_OverwritesExisting(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	args1 := `{"scope":"user","type":"user","name":"prefs","description":"old","content":"first body"}`
	args2 := `{"scope":"user","type":"user","name":"prefs","description":"new","content":"second body"}`
	if _, err := tool.Execute(context.Background(), args1); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args2); err != nil {
		t.Fatalf("second save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "prefs.md"))
	if err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "first body") {
		t.Errorf("overwrite should replace body, not append; got:\n%s", body)
	}
	if !strings.Contains(body, "second body") {
		t.Errorf("expected second body in file:\n%s", body)
	}
	if !strings.Contains(body, "description: new") {
		t.Errorf("description should be overwritten; got:\n%s", body)
	}
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if c := strings.Count(string(idx), "[prefs]"); c != 1 {
		t.Errorf("index should list memory exactly once after overwrite, got %d:\n%s", c, idx)
	}
}

func TestMemorySave_RoutesScopes(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := tool.Execute(context.Background(), `{"scope":"user","type":"user","name":"prefs","description":"x","content":"x"}`); err != nil {
		t.Fatalf("user-scope save: %v", err)
	}
	if _, err := tool.Execute(context.Background(), `{"scope":"project","type":"project","name":"layout","description":"x","content":"x"}`); err != nil {
		t.Fatalf("project-scope save: %v", err)
	}
	userPath := filepath.Join(home, ".yottacode", "memory", "prefs.md")
	if _, err := os.Stat(userPath); err != nil {
		t.Errorf("user-scope file missing at %s: %v", userPath, err)
	}
	projects := filepath.Join(home, ".yottacode", "projects")
	infos, err := os.ReadDir(projects)
	if err != nil {
		t.Fatalf("projects dir missing: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected exactly one project subdir under %s, got %d", projects, len(infos))
	}
	projectMemPath := filepath.Join(projects, infos[0].Name(), "memory", "layout.md")
	if _, err := os.Stat(projectMemPath); err != nil {
		t.Errorf("project-scope file missing at %s: %v", projectMemPath, err)
	}
}

func TestMemorySave_RejectsBadName(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	cases := []struct {
		name string
		args string
	}{
		{"uppercase", `{"scope":"user","type":"user","name":"BadName","description":"x","content":"x"}`},
		{"traversal", `{"scope":"user","type":"user","name":"../escape","description":"x","content":"x"}`},
		{"reserved", `{"scope":"user","type":"user","name":"memory","description":"x","content":"x"}`},
		{"reserved2", `{"scope":"user","type":"user","name":"yottacode","description":"x","content":"x"}`},
		{"empty", `{"scope":"user","type":"user","name":"","description":"x","content":"x"}`},
		{"badscope", `{"scope":"global","type":"user","name":"foo","description":"x","content":"x"}`},
		{"badtype", `{"scope":"user","type":"misc","name":"foo","description":"x","content":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), c.args); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestMemoryForget_DeletesAndUpdatesIndex(t *testing.T) {
	home, cwd := memTestSetup(t)
	save := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"a","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save a: %v", err)
	}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"b","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save b: %v", err)
	}
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	if _, err := forget.Execute(context.Background(), `{"scope":"user","name":"a"}`); err != nil {
		t.Fatalf("forget a: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "a.md")); !os.IsNotExist(err) {
		t.Errorf("expected a.md gone, stat err = %v", err)
	}
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("index missing: %v", err)
	}
	if strings.Contains(string(idx), "[a]") {
		t.Errorf("index still references forgotten memory:\n%s", idx)
	}
	if !strings.Contains(string(idx), "[b]") {
		t.Errorf("index lost remaining memory:\n%s", idx)
	}
}

func TestMemoryForget_RemovesIndexWhenEmpty(t *testing.T) {
	home, cwd := memTestSetup(t)
	save := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"only","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save: %v", err)
	}
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	if _, err := forget.Execute(context.Background(), `{"scope":"user","name":"only"}`); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Errorf("expected MEMORY.md gone after last forget, stat err = %v", err)
	}
}

func TestMemoryForget_MissingNameErrors(t *testing.T) {
	_, cwd := memTestSetup(t)
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	out, err := forget.Execute(context.Background(), `{"scope":"user","name":"never-saved"}`)
	if err == nil {
		t.Fatalf("expected error forgetting nonexistent memory, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "no user memory") {
		t.Errorf("expected 'no user memory' in error, got: %v", err)
	}
}

func TestMemoryTools_RequiresApprovalFalse(t *testing.T) {
	if (&MemorySaveTool{}).RequiresApproval("") {
		t.Errorf("MemorySaveTool.RequiresApproval should be false (silent per design)")
	}
	if (&MemoryForgetTool{}).RequiresApproval("") {
		t.Errorf("MemoryForgetTool.RequiresApproval should be false (silent per design)")
	}
}

func TestMemoryTools_NotParallelSafe(t *testing.T) {
	if (&MemorySaveTool{}).ParallelSafe("") {
		t.Errorf("MemorySaveTool should not be parallel-safe (rewrites index)")
	}
	if (&MemoryForgetTool{}).ParallelSafe("") {
		t.Errorf("MemoryForgetTool should not be parallel-safe (rewrites index)")
	}
}
