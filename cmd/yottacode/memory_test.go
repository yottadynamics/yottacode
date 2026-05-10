package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// seedProjectMemory plants
// ~/.yottacode/projects/<slug(cwd)>/memory/<name>.md.
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
