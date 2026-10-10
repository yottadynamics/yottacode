package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/permissions"
)

// Regression: inside a linked worktree the permissions store writes
// permissions.local.json at the main repo root, so the deny list must cover
// that file or an extra workspace root could be used to self-grant.
func TestDefaultDenyPaths_CoversWorktreeRedirectedLocalFile(t *testing.T) {
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "config", "user.email", "t@e.com")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "f")
	run(repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "linked")
	run(repo, "worktree", "add", "-q", "-b", "feature", wt)

	want := filepath.Join(permissions.StorageRoot(wt), ".yottacode", "permissions.local.json")
	if want == filepath.Join(wt, ".yottacode", "permissions.local.json") {
		t.Fatalf("StorageRoot did not redirect out of the worktree: %s", want)
	}
	for _, p := range DefaultDenyPaths(wt) {
		if p == want {
			return
		}
	}
	t.Fatalf("DefaultDenyPaths(%s) missing repo-root %s", wt, want)
}
