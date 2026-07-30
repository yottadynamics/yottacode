package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRegisterCoreCwdTools_RegistersCoreSet pins the tool set the builder
// produces: the core file/search/git/run tools are present, and the
// delegation tools (Agent/dispatch/integrate) are NOT — a worker built on
// this set can never recurse.
func TestRegisterCoreCwdTools_RegistersCoreSet(t *testing.T) {
	reg := NewRegistry()
	cwd := NewCwdRef(t.TempDir())
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})

	names := reg.Names()
	for _, want := range []string{
		"read_file", "write_file", "edit_file", "edit_anchored", "apply_diff",
		"grep", "glob", "list_dir", "run_bash", "run_tests",
		"git_diff_files", "git_commit",
	} {
		if !names[want] {
			t.Errorf("core tool %q not registered", want)
		}
	}
	for _, absent := range []string{AgentToolName, "dispatch", "integrate", "exit_plan_mode"} {
		if names[absent] {
			t.Errorf("tool %q must NOT be in the core set (workers can't recurse)", absent)
		}
	}
}

// TestRegisterCoreCwdTools_IsolatesByCwd is the property the whole
// worktree-dispatch feature depends on: tools built against a fresh CwdRef
// resolve relative paths under THAT directory, not the process cwd. A
// write through the registered write_file tool must land in the passed
// directory.
func TestRegisterCoreCwdTools_IsolatesByCwd(t *testing.T) {
	dir := t.TempDir()
	cwd := NewCwdRef(dir)
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})

	wf, ok := reg.Get("write_file")
	if !ok {
		t.Fatal("write_file not registered")
	}
	if _, err := wf.Execute(context.Background(), `{"path":"sub/note.txt","content":"hi"}`); err != nil {
		t.Fatalf("write_file Execute: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "note.txt"))
	if err != nil {
		t.Fatalf("expected file under isolated cwd %s: %v", dir, err)
	}
	if string(got) != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
}
