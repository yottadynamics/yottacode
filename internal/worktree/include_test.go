package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{".env", ".env", true},
		{".env", "subdir/.env", false},
		{".env.*", ".env.local", true},
		{".env.*", ".env", false},
		{"**/.env", ".env", true},
		{"**/.env", "config/.env", true},
		{"**/.env", "a/b/c/.env", true},
		{"**/.env", "noenvhere", false},
		{".vscode/settings.json", ".vscode/settings.json", true},
		{".vscode/*.json", ".vscode/settings.json", true},
		{".vscode/*.json", ".vscode/sub/settings.json", false},
		{"**", "anything/anywhere", true},
		{"*.local", "config.local", true},
		{"*.local", "config/file.local", false},
	}
	for _, tc := range tests {
		got, err := matchPattern(tc.pattern, tc.path)
		if err != nil {
			t.Errorf("matchPattern(%q, %q): unexpected error %v", tc.pattern, tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestReadIncludePatternsMissing(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadIncludePatterns(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil patterns for missing file, got %v", got)
	}
}

func TestReadIncludePatterns(t *testing.T) {
	dir := t.TempDir()
	content := "# leading comment\n\n.env\n.env.*\n\n# another comment\n**/.config.json\n"
	if err := os.WriteFile(filepath.Join(dir, IncludeFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadIncludePatterns(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", ".env.*", "**/.config.json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pattern[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCopyIncludedEndToEnd(t *testing.T) {
	repo := t.TempDir()
	// Write some source files
	mustWrite(t, filepath.Join(repo, ".env"), "SECRET=abc\n")
	mustWrite(t, filepath.Join(repo, ".env.local"), "LOCAL=1\n")
	mustWrite(t, filepath.Join(repo, "config", "app.json"), "{}\n")
	mustWrite(t, filepath.Join(repo, "src", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(repo, IncludeFile), ".env\n.env.*\nconfig/*.json\n")

	wt := filepath.Join(t.TempDir(), "feature-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CopyIncluded(repo, wt); err != nil {
		t.Fatal(err)
	}

	// Expect .env, .env.local, config/app.json copied; main.go not copied.
	mustExist(t, filepath.Join(wt, ".env"))
	mustExist(t, filepath.Join(wt, ".env.local"))
	mustExist(t, filepath.Join(wt, "config", "app.json"))
	mustNotExist(t, filepath.Join(wt, "src", "main.go"))
	mustNotExist(t, filepath.Join(wt, IncludeFile)) // not in its own patterns
}

func TestCopyIncludedNoIncludeFile(t *testing.T) {
	repo := t.TempDir()
	wt := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".env"), "S=1\n")
	// No .worktreeinclude -> no copy.
	if err := CopyIncluded(repo, wt); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(wt, ".env"))
}

func TestCopyIncludedSkipsGitAndWorktrees(t *testing.T) {
	// .git/ and .yottacode/worktrees/ must never be walked.
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(repo, ".yottacode", "worktrees", "sibling", ".env"), "X=1\n")
	mustWrite(t, filepath.Join(repo, ".env"), "ROOT=1\n")
	mustWrite(t, filepath.Join(repo, IncludeFile), "**/.env\n")

	wt := t.TempDir()
	if err := CopyIncluded(repo, wt); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(wt, ".env"))
	mustNotExist(t, filepath.Join(wt, ".yottacode", "worktrees", "sibling", ".env"))
	mustNotExist(t, filepath.Join(wt, ".git", "HEAD"))
}

func TestCopyIncludedSkipsSymlinks(t *testing.T) {
	// A symlink whose name matches an include pattern must NOT be
	// followed: copying through it would pull a file from outside the
	// repo into the worktree (data leak) — or, pointed at a device/FIFO,
	// hang io.Copy forever. The symlink is skipped; regular files that
	// also match still copy normally.
	repo := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret"), "TOPSECRET\n")

	mustWrite(t, filepath.Join(repo, ".env"), "OK=1\n")
	// .env.leak -> <outside>/secret. Matches the `.env.*` pattern but
	// resolves outside the repo.
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(repo, ".env.leak")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	mustWrite(t, filepath.Join(repo, IncludeFile), ".env\n.env.*\n")

	wt := t.TempDir()
	if err := CopyIncluded(repo, wt); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(wt, ".env"))         // regular file copied
	mustNotExist(t, filepath.Join(wt, ".env.leak")) // symlink skipped, not followed
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s not to exist", path)
	}
}
