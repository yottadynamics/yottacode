package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWritePath_AllowsCwdRelative(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "sub", "file.txt")
	if err := ValidateWritePath(target, WritePathOptions{Cwd: cwd}); err != nil {
		t.Errorf("expected ok for path under cwd, got %v", err)
	}
}

func TestValidateWritePath_RejectsOutsideCwd(t *testing.T) {
	cwd := t.TempDir()
	target := "/etc/passwd"
	err := ValidateWritePath(target, WritePathOptions{Cwd: cwd})
	if err == nil {
		t.Errorf("expected error for path outside cwd")
	}
	if !strings.Contains(err.Error(), "outside session workspace") {
		t.Errorf("error should mention the workspace boundary: %v", err)
	}
	// Must be the structured sentinel so the TUI can render the
	// inline path-trust elevation modal via errors.As.
	var sentinel *ErrPathOutsideWorkspace
	if !errors.As(err, &sentinel) {
		t.Fatalf("error should be *ErrPathOutsideWorkspace, got %T", err)
	}
	if sentinel.Path != target {
		t.Errorf("sentinel.Path = %q, want %q", sentinel.Path, target)
	}
	if sentinel.Cwd != cwd {
		t.Errorf("sentinel.Cwd = %q, want %q", sentinel.Cwd, cwd)
	}
}

func TestErrPathOutsideWorkspace_MessageMentionsRecovery(t *testing.T) {
	err := &ErrPathOutsideWorkspace{
		Path: "/foo/bar/baz.go",
		Cwd:  "/cwd",
	}
	msg := err.Error()
	// Reject semantics from yottacode-roadmap/folder-trust.md: the
	// model should see the workspace boundary AND a recovery hint
	// (relaunch with --allow-paths covering the parent directory).
	for _, want := range []string{"/foo/bar/baz.go", "/cwd", "--allow-paths"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestValidateWritePath_AllowsExtraRoot(t *testing.T) {
	cwd := t.TempDir()
	allowed := t.TempDir()
	target := filepath.Join(allowed, "ok.txt")
	err := ValidateWritePath(target, WritePathOptions{
		Cwd:          cwd,
		AllowedPaths: []string{allowed},
	})
	if err != nil {
		t.Errorf("expected ok with --allow-paths, got %v", err)
	}
}

func TestValidateWritePath_RejectsTraversalEscape(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "..", "outside.txt")
	err := ValidateWritePath(target, WritePathOptions{Cwd: cwd})
	if err == nil {
		t.Errorf("expected error for ../outside.txt")
	}
}

func TestValidateWritePath_RejectsSymlink(t *testing.T) {
	cwd := t.TempDir()
	link := filepath.Join(cwd, "link.txt")
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := ValidateWritePath(link, WritePathOptions{Cwd: cwd})
	if err == nil {
		t.Errorf("expected error for symlink target")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should mention symlink: %v", err)
	}
}

func TestValidateWritePath_AllowSymlinksOptIn(t *testing.T) {
	cwd := t.TempDir()
	link := filepath.Join(cwd, "ok.txt")
	target := filepath.Join(cwd, "real.txt")
	_ = os.WriteFile(target, []byte("x"), 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := ValidateWritePath(link, WritePathOptions{
		Cwd:           cwd,
		AllowSymlinks: true,
	})
	if err != nil {
		t.Errorf("expected ok with AllowSymlinks=true, got %v", err)
	}
}

func TestValidateWritePath_DenyListWins(t *testing.T) {
	cwd := t.TempDir()
	deny := filepath.Join(cwd, "secret")
	if err := os.MkdirAll(deny, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(deny, "evil.txt")
	err := ValidateWritePath(target, WritePathOptions{
		Cwd:       cwd,
		DenyExact: []string{deny},
	})
	if err == nil {
		t.Errorf("expected deny-list error even though path is under cwd")
	}
	if !strings.Contains(err.Error(), "deny list") {
		t.Errorf("error should mention deny list: %v", err)
	}
}

func TestValidateWritePath_RejectsEmptyAndNUL(t *testing.T) {
	cwd := t.TempDir()
	if err := ValidateWritePath("", WritePathOptions{Cwd: cwd}); err == nil {
		t.Errorf("expected error for empty path")
	}
	if err := ValidateWritePath("a\x00b", WritePathOptions{Cwd: cwd}); err == nil {
		t.Errorf("expected error for NUL in path")
	}
}

func TestDefaultDenyPaths_IncludesYottacodeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	got := DefaultDenyPaths(cwd)
	mustHave := []string{
		filepath.Join(home, ".yottacode", "sessions"),
		filepath.Join(home, ".yottacode", "memory"),
		filepath.Join(home, ".yottacode", "projects"),
		filepath.Join(home, ".yottacode", "USER.md"),
		filepath.Join(home, ".yottacode", "trusted-roots.json"),
		filepath.Join(cwd, ".yottacode", "permissions.json"),
		filepath.Join(cwd, ".yottacode", "permissions.local.json"),
		filepath.Join(cwd, ".git", "HEAD"),
		filepath.Join(cwd, ".git", "config"),
	}
	for _, want := range mustHave {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultDenyPaths missing %q in %v", want, got)
		}
	}

	// YOTTACODE.md was removed from the deny list — the agent owns
	// keeping it fresh, same role CLAUDE.md plays for Claude Code.
	// Approval modal is the gate. Asserting absence so a regression
	// would re-add it loudly.
	mustNotHave := filepath.Join(cwd, ".yottacode", "YOTTACODE.md")
	for _, p := range got {
		if p == mustNotHave {
			t.Errorf("YOTTACODE.md should NOT be in deny list (modal gates writes); got %v", got)
		}
	}
}

func TestDefaultDenyPaths_AllowsProjectMd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	target := filepath.Join(cwd, ".yottacode", "YOTTACODE.md")
	err := ValidateWritePath(target, WritePathOptions{
		Cwd:       cwd,
		DenyExact: DefaultDenyPaths(cwd),
	})
	if err != nil {
		t.Errorf("YOTTACODE.md should be writable (gated by approval modal, not deny list): %v", err)
	}
}

func TestDefaultDenyPaths_ExcludesGitHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := DefaultDenyPaths("/repo")
	for _, p := range got {
		if strings.HasSuffix(p, "/.git/hooks") {
			t.Errorf("hooks should NOT be in deny list: %v", got)
		}
	}
}

func TestValidateWritePath_AppStateDeniedEvenInsideHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := home // user runs yottacode from $HOME

	// Self-grant via the user-scope memory/ dir is the canonical
	// "model writes a file it shouldn't be able to via the generic
	// write_file tool" target. Per-project permissions.json /
	// permissions.local.json are also covered (see the cwd-scoped
	// case below).
	target := filepath.Join(home, ".yottacode", "memory", "evil.md")
	err := ValidateWritePath(target, WritePathOptions{
		Cwd:       cwd,
		DenyExact: DefaultDenyPaths(cwd),
	})
	if err == nil {
		t.Errorf("expected deny-list error for ~/.yottacode/memory/evil.md even when cwd=$HOME")
	}

	permsTarget := filepath.Join(cwd, ".yottacode", "permissions.json")
	err = ValidateWritePath(permsTarget, WritePathOptions{
		Cwd:       cwd,
		DenyExact: DefaultDenyPaths(cwd),
	})
	if err == nil {
		t.Errorf("expected deny-list error for .yottacode/permissions.json (model self-grant)")
	}
}

// ~/.yottacode/auth/ holds OAuth bearer + refresh tokens. Reads
// would let the model exfiltrate them; writes would let the model
// corrupt the OAuth flow's exclusive ownership. Both deny lists
// must include the directory so a future per-provider auth file
// inherits the protection without a code change.
func TestDefaultDenyReadPaths_IncludesAuthDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := DefaultDenyReadPaths("")
	want := filepath.Join(home, ".yottacode", "auth")
	for _, p := range got {
		if p == want {
			return
		}
	}
	t.Errorf("DefaultDenyReadPaths missing %q; got %v", want, got)
}

func TestDefaultDenyPaths_IncludesAuthDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := DefaultDenyPaths("")
	want := filepath.Join(home, ".yottacode", "auth")
	for _, p := range got {
		if p == want {
			return
		}
	}
	t.Errorf("DefaultDenyPaths missing %q; got %v", want, got)
}
