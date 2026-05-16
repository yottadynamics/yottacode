package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirAndBranch(t *testing.T) {
	// Pin HOME so the test is deterministic across machines.
	t.Setenv("HOME", "/tmp/fake-home")
	repo := "/tmp/repo"
	slug := RepoSlug(repo)
	got := Dir(repo, "feature-x")
	want := "/tmp/fake-home/.yottacode/worktrees/" + slug + "/feature-x"
	if got != want {
		t.Fatalf("Dir: got %q want %q", got, want)
	}
	if got, want := Branch("feature-x"), "worktree-feature-x"; got != want {
		t.Fatalf("Branch: got %q want %q", got, want)
	}
}

func TestRepoSlug(t *testing.T) {
	a := RepoSlug("/home/me/proj")
	b := RepoSlug("/home/me/proj")
	if a != b {
		t.Fatalf("RepoSlug should be deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "proj-") {
		t.Errorf("expected basename prefix `proj-`, got %q", a)
	}
	if len(a) != len("proj-")+8 {
		t.Errorf("expected basename + 8-char hash, got %q", a)
	}
	// Different absolute paths with the same basename get different slugs.
	c := RepoSlug("/elsewhere/proj")
	if c == a {
		t.Errorf("slugs for distinct repos must differ: %q == %q", a, c)
	}
	if !strings.HasPrefix(c, "proj-") {
		t.Errorf("expected basename prefix `proj-`, got %q", c)
	}
	// Degenerate inputs don't panic.
	if got := RepoSlug("/"); !strings.HasPrefix(got, "repo-") {
		t.Errorf("RepoSlug(/) should fall back to `repo-...`, got %q", got)
	}
}

func TestIsWorktreePath(t *testing.T) {
	t.Setenv("HOME", "/tmp/fake-home")
	repo := "/tmp/repo"
	slug := RepoSlug(repo)
	base := "/tmp/fake-home/.yottacode/worktrees/" + slug
	tests := []struct {
		path    string
		wantOK  bool
		wantName string
	}{
		{"/tmp/repo", false, ""},
		{"/tmp/repo/main.go", false, ""},
		{base + "/feature-x", true, "feature-x"},
		{base + "/feature-x/sub/file.go", true, "feature-x"},
		// Different repo's worktree dir → not recognized for this repo.
		{"/tmp/fake-home/.yottacode/worktrees/" + RepoSlug("/other") + "/foo", false, ""},
		// Slug dir itself, no name → not a worktree path.
		{base, false, ""},
	}
	for _, tc := range tests {
		name, ok := IsWorktreePath(repo, tc.path)
		if ok != tc.wantOK || name != tc.wantName {
			t.Errorf("IsWorktreePath(%q): got (%q, %v), want (%q, %v)",
				tc.path, name, ok, tc.wantName, tc.wantOK)
		}
	}
}

func TestNormalizeForRule(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	tests := []struct {
		path string
		want string
	}{
		// File inside a worktree → name segment becomes `*`.
		{
			"/home/me/.yottacode/worktrees/proj-1a2b3c4d/noble-hopping-salmon/internal/agent/loop.go",
			"/home/me/.yottacode/worktrees/proj-1a2b3c4d/*/internal/agent/loop.go",
		},
		// Worktree dir itself → name segment becomes `*`.
		{
			"/home/me/.yottacode/worktrees/proj-1a2b3c4d/feature-x",
			"/home/me/.yottacode/worktrees/proj-1a2b3c4d/*",
		},
		// Main checkout / unrelated paths pass through unchanged.
		{"/home/me/proj/internal/foo.go", "/home/me/proj/internal/foo.go"},
		{"/tmp/something", "/tmp/something"},
		// Worktrees dir without slug returns as-is (not a worktree path).
		{"/home/me/.yottacode/worktrees", "/home/me/.yottacode/worktrees"},
	}
	for _, tc := range tests {
		got := NormalizeForRule(tc.path)
		if got != tc.want {
			t.Errorf("NormalizeForRule(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestIsAnyWorktreePath(t *testing.T) {
	t.Setenv("HOME", "/tmp/fake-home")
	tests := []struct {
		path     string
		wantOK   bool
		wantSlug string
		wantName string
	}{
		{"/tmp/fake-home/.yottacode/worktrees/proj-aaaaaaaa/feature-x", true, "proj-aaaaaaaa", "feature-x"},
		{"/tmp/fake-home/.yottacode/worktrees/proj-aaaaaaaa/feature-x/sub.go", true, "proj-aaaaaaaa", "feature-x"},
		{"/tmp/fake-home/.yottacode/worktrees/proj-aaaaaaaa", false, "", ""}, // slug-only
		{"/tmp/fake-home/.yottacode/worktrees", false, "", ""},
		{"/home/me/some-repo/.yottacode/worktrees/foo/bar", false, "", ""}, // not under home
	}
	for _, tc := range tests {
		slug, name, ok := IsAnyWorktreePath(tc.path)
		if ok != tc.wantOK || slug != tc.wantSlug || name != tc.wantName {
			t.Errorf("IsAnyWorktreePath(%q): got (%q, %q, %v), want (%q, %q, %v)",
				tc.path, slug, name, ok, tc.wantSlug, tc.wantName, tc.wantOK)
		}
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"feature-x", "fix_bug", "abc123", "A", "f-1_2-3"}
	bad := []string{"", ".hidden", "-leading-dash", "with space", "slash/inside", strings.Repeat("a", 65)}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q): unexpected error %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q): expected error, got nil", n)
		}
	}
}

func TestRepoRootAndList(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()

	root, err := RepoRoot(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	// macOS /var → /private/var symlink; resolve via EvalSymlinks for comparison.
	if !samePath(root, repo) {
		t.Fatalf("RepoRoot: got %q want %q", root, repo)
	}

	infos, err := List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 worktree (main), got %d: %+v", len(infos), infos)
	}
	if !samePath(infos[0].Path, repo) {
		t.Fatalf("main worktree path: got %q want %q", infos[0].Path, repo)
	}
	if infos[0].Branch == "" {
		t.Fatalf("main worktree should have a branch")
	}

	// Add a worktree the long way (so we don't depend on agent tools here).
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feature-x")
	mustRun(t, repo, "git", "worktree", "add", "-b", "worktree-feature-x", wt)

	infos, err = List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 worktrees after add, got %d: %+v", len(infos), infos)
	}
	found := false
	for _, w := range infos {
		if samePath(w.Path, wt) && w.Branch == "worktree-feature-x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new worktree not found: %+v", infos)
	}
}

func TestGenerateAvoidsCollision(t *testing.T) {
	repo := mkRepo(t)
	ctx := context.Background()
	// No existing worktrees yet — generated name must be valid.
	name, err := Generate(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateName(name); err != nil {
		t.Fatalf("Generate returned invalid name %q: %v", name, err)
	}
	// Generate a few — they should all be valid; uniqueness across runs
	// not guaranteed but each must validate.
	for i := 0; i < 5; i++ {
		n, err := Generate(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateName(n); err != nil {
			t.Fatalf("Generate iter %d: invalid name %q: %v", i, n, err)
		}
	}
}

// mkRepo creates a fresh git repo with one initial commit and returns
// the absolute path. The repo is in t.TempDir so it's cleaned up
// automatically.
func mkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Resolve symlinks so paths reported by git match what we passed.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, resolved, "git", "init", "-q")
	mustRun(t, resolved, "git", "config", "user.name", "Test")
	mustRun(t, resolved, "git", "config", "user.email", "test@example.com")
	mustRun(t, resolved, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(resolved, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, resolved, "git", "add", "README.md")
	mustRun(t, resolved, "git", "commit", "-q", "-m", "init")
	return resolved
}

func mustRun(t *testing.T, cwd string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := gitOut(ctx, cwd, args[1:]...); err != nil {
		t.Fatalf("%s %v: %v", args[0], args[1:], err)
	}
}

func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea != nil || eb != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
