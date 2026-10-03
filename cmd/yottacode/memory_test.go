package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
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
	// Keep recall tests independent of any Ollama service configured on the
	// developer host; semantic availability has dedicated httptest coverage.
	t.Setenv("OLLAMA_HOST", "http://localhost:1")
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

func TestMemoryCommandRegistration(t *testing.T) {
	cmd := newCLI()
	searchCmd, _, err := cmd.Find([]string{"memory", "search"})
	if err != nil {
		t.Fatalf("find memory search: %v", err)
	}
	if searchCmd != nil && searchCmd.Name() == "search" {
		t.Fatal("memory search command should not be registered")
	}
	recallCmd, _, err := cmd.Find([]string{"memory", "recall"})
	if err != nil {
		t.Fatalf("find memory recall: %v", err)
	}
	if recallCmd == nil || recallCmd.Name() != "recall" {
		t.Fatal("memory recall command should be registered")
	}
}

func TestMemoryRecallJSONAndScope(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "project-match", "deployment kubernetes production")
	userDir, err := memory.UserMemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "user-match.md"), []byte("---\nname: user-match\ntype: user\ndescription: personal testing preference\n---\nUse table-driven tests.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "recall", "--query", "testing preference", "--scope", "user", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var response memoryRecallResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out.String())
	}
	if response.Query != "testing preference" || len(response.Results) == 0 {
		t.Fatalf("unexpected response: %+v", response)
	}
	for _, result := range response.Results {
		if result.Scope != "user" {
			t.Fatalf("user scope returned %+v", result)
		}
	}
	if response.Results[0].Body != "Use table-driven tests." {
		t.Fatalf("body = %q", response.Results[0].Body)
	}
}

func TestMemoryRecallEmptyJSONUsesArray(t *testing.T) {
	withCwdAndHome(t)
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "recall", "--query", "anything", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), `"results": []`) {
		t.Fatalf("empty JSON must use an array: %s", out.String())
	}
}

func TestMemoryRecallAllShadowsUserAndHonorsTopK(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "shared", "project deployment answer")
	seedProjectMemory(t, cwd, "other", "unrelated body")
	userDir, err := memory.UserMemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "shared.md"), []byte("---\nname: shared\ntype: user\ndescription: x\n---\nuser deployment answer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "recall", "--query", "deployment", "--top-k", "1", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var response memoryRecallResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Name != "shared" || response.Results[0].Scope != "project" {
		t.Fatalf("shadowed/top-k response = %+v", response.Results)
	}
}

func TestMemoryRecallMatchesProductionSelector(t *testing.T) {
	cwd := withCwdAndHome(t)
	seedProjectMemory(t, cwd, "deploy", "kubernetes deployment production")
	seedProjectMemory(t, cwd, "testing", "table driven unit tests")
	loaded, err := memory.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Retrieval
	cfg.Enabled = true
	cfg.Strategy = "bm25"
	want := memory.SelectWithEmbeddingsScored(context.Background(), memory.EffectiveEntries(loaded.UserMemories, loaded.ProjectMemories), "production deployment", cfg, nil)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "recall", "--query", "production deployment", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var response memoryRecallResponse
	if err := json.Unmarshal([]byte(out.String()), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != len(want) {
		t.Fatalf("result count = %d, want %d", len(response.Results), len(want))
	}
	for i := range want {
		if response.Results[i].Name != want[i].Entry.Name || response.Results[i].Score != want[i].Score {
			t.Fatalf("result %d = %+v, want %s score %v", i, response.Results[i], want[i].Entry.Name, want[i].Score)
		}
	}
	prompt := memory.SystemPromptForSemantic(context.Background(), "base", loaded, "production deployment", cfg, nil)
	for _, result := range want {
		if !strings.Contains(prompt, result.Entry.Body) {
			t.Fatalf("live prompt omitted selected memory %q", result.Entry.Name)
		}
	}
}

func TestMemoryRecallValidation(t *testing.T) {
	withCwdAndHome(t)
	for _, args := range [][]string{
		{"memory", "recall"},
		{"memory", "recall", "--query", "q", "--scope", "bad"},
		{"memory", "recall", "--query", "q", "--format", "yaml"},
		{"memory", "recall", "--query", "q", "--top-k", "0"},
	} {
		cmd := newCLI()
		cmd.SilenceUsage = true
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("expected validation error for %v", args)
		}
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

func TestMemoryArchiveListAndPrune(t *testing.T) {
	cwd := withCwdAndHome(t)
	path := seedProjectMemory(t, cwd, "archived", "v1")
	archivePath, err := memory.ArchivePrior(path, "100")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -120)
	if err := os.Chtimes(archivePath, old, old); err != nil {
		t.Fatal(err)
	}

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "archive", "list", "--scope", "project"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list execute: %v", err)
	}
	if !strings.Contains(out.String(), "project\tarchived\t1") {
		t.Fatalf("archive list output = %q", out.String())
	}

	cmd = newCLI()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"memory", "archive", "prune", "--scope", "project", "--older-than-days", "90"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run prune execute: %v", err)
	}
	if !strings.Contains(out.String(), "would delete 1 archive") {
		t.Fatalf("dry-run output = %q", out.String())
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("dry-run should keep archive: %v", err)
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
