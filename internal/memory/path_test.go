package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectSlug_GitRemoteHTTPS(t *testing.T) {
	got := slugifyGitURL("https://github.com/user/repo.git")
	if want := "github-com-user-repo"; got != want {
		t.Errorf("slugifyGitURL(https) = %q, want %q", got, want)
	}
}

func TestProjectSlug_GitRemoteSCP(t *testing.T) {
	got := slugifyGitURL("git@github.com:user/repo.git")
	if want := "github-com-user-repo"; got != want {
		t.Errorf("slugifyGitURL(scp) = %q, want %q", got, want)
	}
}

func TestProjectSlug_GitRemoteSSH(t *testing.T) {
	got := slugifyGitURL("ssh://git@gitlab.com/group/repo.git")
	if want := "gitlab-com-group-repo"; got != want {
		t.Errorf("slugifyGitURL(ssh) = %q, want %q", got, want)
	}
}

func TestProjectSlug_DropsTokenCredentials(t *testing.T) {
	got := slugifyGitURL("https://oauth2:abc123@gitlab.com/group/repo.git")
	if want := "gitlab-com-group-repo"; got != want {
		t.Errorf("slugifyGitURL(token) = %q, want %q", got, want)
	}
}

func TestProjectSlug_FallsBackToBasename(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "MyApp")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := ProjectSlug(cwd)
	if got != "myapp" {
		t.Errorf("ProjectSlug = %q, want %q", got, "myapp")
	}
}

func TestProjectSlug_EmptyCwdReturnsDefault(t *testing.T) {
	if got := ProjectSlug(""); got != "default" {
		t.Errorf("ProjectSlug(\"\") = %q, want %q", got, "default")
	}
}

func TestProjectSlug_UsesGitSlugCache(t *testing.T) {
	// A directory with no git remote would normally fall back to its
	// slugified basename. Pre-seeding the cache proves ProjectSlug
	// consults it first — and thus avoids the per-turn git subprocess.
	cwd := filepath.Join(t.TempDir(), "cached-proj")
	gitSlugCacheMu.Lock()
	gitSlugCache[cwd] = "github-com-acme-widget"
	gitSlugCacheMu.Unlock()
	t.Cleanup(func() {
		gitSlugCacheMu.Lock()
		delete(gitSlugCache, cwd)
		gitSlugCacheMu.Unlock()
	})
	if got := ProjectSlug(cwd); got != "github-com-acme-widget" {
		t.Errorf("ProjectSlug should return the cached git slug, not the basename; got %q", got)
	}
}

func TestProjectSlugFromGit_CachesNegativeResult(t *testing.T) {
	// A non-repo resolves to "" and that result is cached, so a second
	// lookup is served from the cache rather than forking git again.
	cwd := t.TempDir() // bare temp dir, no .git
	if got := projectSlugFromGit(cwd); got != "" {
		t.Fatalf("non-repo should resolve to empty slug; got %q", got)
	}
	t.Cleanup(func() {
		gitSlugCacheMu.Lock()
		delete(gitSlugCache, cwd)
		gitSlugCacheMu.Unlock()
	})
	gitSlugCacheMu.RLock()
	cached, ok := gitSlugCache[cwd]
	gitSlugCacheMu.RUnlock()
	if !ok || cached != "" {
		t.Errorf("negative git result should be cached as \"\"; ok=%v cached=%q", ok, cached)
	}
}

func TestProjectSlug_HandlesSpacesAndUppercase(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "My  Cool Project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := ProjectSlug(cwd); got != "my-cool-project" {
		t.Errorf("ProjectSlug = %q, want %q", got, "my-cool-project")
	}
}

func TestSlugify_Idempotent(t *testing.T) {
	in := "GitHub.com/User/Repo"
	once := slugify(in)
	twice := slugify(once)
	if once != twice {
		t.Errorf("slugify not idempotent: %q → %q → %q", in, once, twice)
	}
	if once != "github-com-user-repo" {
		t.Errorf("slugify(%q) = %q, want %q", in, once, "github-com-user-repo")
	}
}

func TestUserMemoryDir_RootedUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	got, err := UserMemoryDir()
	if err != nil {
		t.Fatalf("UserMemoryDir: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "memory", "user")
	if got != want {
		t.Errorf("UserMemoryDir = %q, want %q", got, want)
	}
}

func TestMemoryRoot_HonorsYottacodeHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	override := t.TempDir()
	t.Setenv("YOTTACODE_HOME", override)

	user, err := UserMemoryDir()
	if err != nil {
		t.Fatalf("UserMemoryDir: %v", err)
	}
	if want := filepath.Join(override, "memory", "user"); user != want {
		t.Errorf("UserMemoryDir = %q, want %q", user, want)
	}

	cwd := filepath.Join(t.TempDir(), "myapp")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	proj, err := ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	if want := filepath.Join(override, "memory", "projects", "myapp"); proj != want {
		t.Errorf("ProjectMemoryDir = %q, want %q", proj, want)
	}
}

func TestProjectMemoryDir_UsesProjectSlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	cwd := filepath.Join(t.TempDir(), "myapp")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "memory", "projects", "myapp")
	if got != want {
		t.Errorf("ProjectMemoryDir = %q, want %q", got, want)
	}
}

func TestMemoryFilePath_ValidUserScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	got, err := MemoryFilePath("user", "code-style", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "/memory/user/code-style.md") {
		t.Errorf("MemoryFilePath = %q, want suffix /memory/user/code-style.md", got)
	}
}

func TestMemoryFilePath_ValidProjectScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := filepath.Join(t.TempDir(), "demo")
	_ = os.MkdirAll(cwd, 0o755)
	got, err := MemoryFilePath("project", "tooling", cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "/memory/projects/demo/tooling.md") {
		t.Errorf("MemoryFilePath = %q, want suffix /memory/projects/demo/tooling.md", got)
	}
}

func TestMemoryFilePath_RejectsBadNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	bad := []string{
		"",                      // empty
		"Foo",                   // uppercase
		"foo bar",               // space
		"foo/bar",               // slash
		"../escape",             // traversal
		"foo.md",                // dot
		"-leading-hyphen",       // starts with hyphen
		strings.Repeat("a", 65), // too long
	}
	for _, name := range bad {
		if _, err := MemoryFilePath("user", name, ""); err == nil {
			t.Errorf("MemoryFilePath(user, %q) should have errored", name)
		}
	}
}

func TestMemoryFilePath_RejectsReservedNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	for _, name := range []string{"user", "project", "projects", "memory", "index", "sessions", "subagents", "yottacode", "feedback", "reference"} {
		if _, err := MemoryFilePath("user", name, ""); err == nil {
			t.Errorf("MemoryFilePath(user, %q) should have errored on reserved name", name)
		}
	}
}

func TestMemoryFilePath_RejectsBadScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	if _, err := MemoryFilePath("global", "ok", ""); err == nil {
		t.Errorf("MemoryFilePath(global, ...) should reject unknown scope")
	}
}

func TestMemoryFilePath_RejectsSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	memDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(memDir, "evil.md")
	if err := os.Symlink("/etc/passwd", target); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := MemoryFilePath("user", "evil", ""); err == nil {
		t.Errorf("MemoryFilePath should refuse to return a symlinked path")
	}
}

func TestEnsureUserMemoryDir_Idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	got, err := EnsureUserMemoryDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "memory", "user")
	if got != want {
		t.Errorf("EnsureUserMemoryDir = %q, want %q", got, want)
	}
	if _, err := EnsureUserMemoryDir(); err != nil {
		t.Errorf("second call errored: %v", err)
	}
}

func TestEnsureProjectMemoryDir_Idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	cwd := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := EnsureProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "memory", "projects", "demo")
	if got != want {
		t.Errorf("EnsureProjectMemoryDir = %q, want %q", got, want)
	}
	if _, err := EnsureProjectMemoryDir(cwd); err != nil {
		t.Errorf("second call errored: %v", err)
	}
}
