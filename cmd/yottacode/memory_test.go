package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// seedProjectMemory plants
// ~/.yottacode/memory/projects/<slug(cwd)>/<name>.md.
func seedProjectMemory(t *testing.T, cwd, name, body string) string {
	t.Helper()
	dir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name+".md")
	contents := "---\nname: " + name + "\ntype: project\ndescription: x\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func withCwdAndHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// Memory paths now honor $YOTTACODE_HOME; clear it so a developer/CI
	// with the override exported doesn't read or mutate the real store.
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return cwd
}

func TestMemoryList_RendersEntriesSortedByName(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "bravo", "second")
	seedProjectMemory(t, cwd, "alpha", "first")

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := out.String()
	if i, j := strings.Index(body, "alpha"), strings.Index(body, "bravo"); i < 0 || j < 0 || i > j {
		t.Errorf("expected alpha before bravo; got %q", body)
	}
}

func TestMemoryList_EmptyFolderPrintsHint(t *testing.T) {
	withCwdAndHome(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "(no memories)") {
		t.Errorf("empty folder should hint clearly; got %q", out.String())
	}
}

func TestMemoryForget_DeletesEntryFile(t *testing.T) {
	cwd := withCwdAndHome(t)
	path := seedProjectMemory(t, cwd, "drop-me", "fact")

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "forget", "drop-me"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected memory file to be deleted; stat err = %v", err)
	}
	if !strings.Contains(out.String(), "forgot project memory drop-me") {
		t.Errorf("expected confirmation line; got %q", out.String())
	}
}

func TestMemoryForget_UnknownEntryErrors(t *testing.T) {
	withCwdAndHome(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "forget", "never-existed"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for unknown memory; got nil")
	}
	if !strings.Contains(err.Error(), "never-existed") {
		t.Errorf("error should reference the name; got %q", err)
	}
}

func TestMemoryForget_RejectsBadSlug(t *testing.T) {
	withCwdAndHome(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "forget", "../escape"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected slug-validation error; got nil")
	}
}

func TestMemoryList_UserScope(t *testing.T) {
	withCwdAndHome(t)
	dir, err := memory.UserMemoryDir()
	if err != nil {
		t.Fatalf("UserMemoryDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u-fact.md"),
		[]byte("---\nname: u-fact\ntype: user\ndescription: x\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "list", "--scope", "user"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "u-fact") {
		t.Errorf("user-scope list should include u-fact; got %q", out.String())
	}
}

func TestMemoryAudit_ReportsCurationQueue(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "raw-note", "User prefers concise answers")
	path, err := memory.MemoryFilePath("project", "raw-note", cwd)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(mustReadFile(t, path), "type: project", "type: note", 1)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "audit"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := out.String()
	for _, want := range []string{"memories: 1 total", "quick-note", "raw-note", "action", "memory_get"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit output missing %q: %q", want, body)
		}
	}
}

func TestMemoryAuditPlan_GroupsCurationQueue(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "raw-note", "User prefers concise answers")
	path, err := memory.MemoryFilePath("project", "raw-note", cwd)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(mustReadFile(t, path), "type: project", "type: note", 1)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "audit", "--plan"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := out.String()
	for _, want := range []string{"curation plan:", "Promote or delete quick notes", "project/raw-note"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan output missing %q: %q", want, body)
		}
	}
}

func TestMemoryAuditPropose_DraftsSubjectiveCuration(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "raw-note", "User prefers concise answers")
	path, err := memory.MemoryFilePath("project", "raw-note", cwd)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(mustReadFile(t, path), "type: project", "type: note", 1)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "audit", "--propose"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := out.String()
	for _, want := range []string{"curation proposals:", "not applied", "project/raw-note", "promote-candidate"} {
		if !strings.Contains(body, want) {
			t.Errorf("proposal output missing %q: %q", want, body)
		}
	}
}

func TestMemoryHealth_RendersCompactCounts(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "raw-note", "User prefers concise answers")
	path, err := memory.MemoryFilePath("project", "raw-note", cwd)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(mustReadFile(t, path), "type: project", "type: note", 1)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "health"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := out.String()
	for _, want := range []string{"memory health: 1 memories", "quick notes: 1", "vague bodies: 0", "duplicates: 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("health output missing %q: %q", want, body)
		}
	}
}

func TestMemoryAudit_CleanStore(t *testing.T) {
	withCwdAndHome(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "audit"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "memory store looks curated") {
		t.Errorf("clean audit should say store looks curated; got %q", out.String())
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
