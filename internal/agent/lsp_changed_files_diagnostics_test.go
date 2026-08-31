package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestChangedSourceFilesIncludesStagedUnstagedAndUntracked(t *testing.T) {
	repo := initGitRepoForChangedFilesTest(t)
	writeFile(t, repo, "tracked.go", "package main\nvar tracked = 1\n")
	gitAddAndCommit(t, repo, "tracked.go")

	writeFile(t, repo, "tracked.go", "package main\nvar tracked = 2\n")
	writeFile(t, repo, "staged.go", "package main\nvar staged = 1\n")
	mustRunGit(t, repo, "add", "staged.go")
	writeFile(t, repo, "untracked.go", "package main\nvar untracked = 1\n")
	writeFile(t, repo, "README.md", "ignored\n")

	got, err := changedSourceFiles(context.Background(), repo, 0)
	if err != nil {
		t.Fatalf("changedSourceFiles: %v", err)
	}
	want := []string{"staged.go", "tracked.go", "untracked.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedSourceFiles() = %#v, want %#v", got, want)
	}
}

func TestChangedSourceFilesDeduplicatesAndRespectsMaxFiles(t *testing.T) {
	repo := initGitRepoForChangedFilesTest(t)
	writeFile(t, repo, "a.go", "package main\nvar a = 1\n")
	writeFile(t, repo, "b.go", "package main\nvar b = 1\n")
	gitAddAndCommit(t, repo, "a.go", "b.go")

	writeFile(t, repo, "a.go", "package main\nvar a = 2\n")
	mustRunGit(t, repo, "add", "a.go")
	writeFile(t, repo, "b.go", "package main\nvar b = 2\n")

	got, err := changedSourceFiles(context.Background(), repo, 1)
	if err != nil {
		t.Fatalf("changedSourceFiles: %v", err)
	}
	want := []string{"a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedSourceFiles() = %#v, want %#v", got, want)
	}
}

func TestChangedSourceFilesSkipsGeneratedLocalArtifacts(t *testing.T) {
	repo := initGitRepoForChangedFilesTest(t)
	writeFile(t, repo, "tracked.go", "package main\nvar tracked = 1\n")
	gitAddAndCommit(t, repo, "tracked.go")
	writeFile(t, repo, "tracked.go", "package main\nvar tracked = 2\n")
	writeFile(t, repo, ".cache/generated.go", "package cache\n")
	writeFile(t, repo, ".config/go/telemetry/local/generated.go", "package local\n")
	writeFile(t, repo, "go/pkg/mod/example.com/mod@v1.0.0/generated.go", "package mod\n")

	got, err := changedSourceFiles(context.Background(), repo, 0)
	if err != nil {
		t.Fatalf("changedSourceFiles: %v", err)
	}
	want := []string{"tracked.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedSourceFiles() = %#v, want %#v", got, want)
	}
}

func initGitRepoForChangedFilesTest(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.name", "yottacode test")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")
	return repo
}

func gitAddAndCommit(t *testing.T, repo string, paths ...string) {
	t.Helper()
	args := append([]string{"add"}, paths...)
	mustRunGit(t, repo, args...)
	mustRunGit(t, repo, "commit", "-m", "test")
}

func mustRunGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func TestChangedSourceFilesWorksFromNestedDir(t *testing.T) {
	repo := initGitRepoForChangedFilesTest(t)
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	writeFile(t, repo, "pkg/nested.go", "package pkg\nvar nested = 1\n")
	gitAddAndCommit(t, repo, "pkg/nested.go")
	writeFile(t, repo, "pkg/nested.go", "package pkg\nvar nested = 2\n")

	got, err := changedSourceFiles(context.Background(), filepath.Join(repo, "pkg"), 0)
	if err != nil {
		t.Fatalf("changedSourceFiles: %v", err)
	}
	want := []string{"pkg/nested.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedSourceFiles() = %#v, want %#v", got, want)
	}
}

func TestFormatChangedFilesDiagnosticsSummarizesCleanFiles(t *testing.T) {
	results := []changedFileDiagnosticsResult{
		{Path: "a.go", Snapshot: lspci.DiagnosticsSnapshot{Published: true}},
		{Path: "b.go", Snapshot: lspci.DiagnosticsSnapshot{Published: true}},
		{Path: "c.go", Snapshot: lspci.DiagnosticsSnapshot{Published: true}},
	}

	got := formatChangedFilesDiagnostics(results)
	for _, want := range []string{
		"checked 3 changed LSP source files",
		"✓ clean: no diagnostics in 3 files",
		"files: a.go, b.go, c.go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("clean summary missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "(no diagnostics)") {
		t.Fatalf("clean summary should collapse repeated no-diagnostic rows: %q", got)
	}
}

func TestFormatChangedFilesDiagnosticsReportsIssuesAndSkippedStates(t *testing.T) {
	results := []changedFileDiagnosticsResult{
		{Path: "clean.go", Snapshot: lspci.DiagnosticsSnapshot{Published: true}},
		{Path: "bad.go", Snapshot: lspci.DiagnosticsSnapshot{Published: true, Diagnostics: []lspci.Diagnostic{{
			Path:      "bad.go",
			Line:      1,
			Character: 2,
			Severity:  "error",
			Source:    "gopls",
			Message:   "undefined: thing",
		}}}},
		{Path: "slow.go", Snapshot: lspci.DiagnosticsSnapshot{Published: false}},
		{Path: "missing.py", Unavailable: `unavailable: Python language server "pyright-langserver" not found on PATH. install pyright`},
	}

	got := formatChangedFilesDiagnostics(results)
	for _, want := range []string{
		"checked 4 changed LSP source files",
		"⚠ 1 diagnostic in 1 file",
		"✓ clean: 1 file",
		"○ pending: diagnostics not published for 1 file before timeout",
		"pending files: slow.go",
		"○ skipped: LSP unavailable for 1 file",
		"missing.py: unavailable: Python language server",
		"bad.go:2:3\terror source=gopls\tundefined: thing",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics summary missing %q: %q", want, got)
		}
	}
}
