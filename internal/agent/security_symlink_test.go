package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestValidateWritePath_RejectsSymlinkedParent is a regression for the
// release audit's writepath-parent-symlink-confinement-bypass finding: a
// symlinked PARENT directory must not launder a lexically-in-cwd path into
// a write outside the workspace. The old validator only Lstat'd the leaf.
func TestValidateWritePath_RejectsSymlinkedParent(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Lexically under cwd, but link -> outside, so it really writes there.
	target := filepath.Join(link, "evil.txt")
	if err := ValidateWritePath(target, WritePathOptions{Cwd: NewCwdRef(cwd)}); err == nil {
		t.Fatalf("symlinked-parent write escaped cwd confinement")
	}

	// A genuine in-cwd write must still pass (no false positive).
	ok := filepath.Join(cwd, "sub", "file.txt")
	if err := ValidateWritePath(ok, WritePathOptions{Cwd: NewCwdRef(cwd)}); err != nil {
		t.Errorf("legitimate in-cwd write rejected: %v", err)
	}
}

// TestValidateWritePath_DenyListSurvivesSymlinkedCwd is a regression for
// the diff review: when cwd traverses a symlink (macOS /tmp, a symlinked
// home), the deny list — built from the cwd string — is in the lexical
// frame while the resolved write target is canonical. Checking only the
// resolved frame let a write to .yottacode/permissions.json slip through
// (self-grant). The deny check must hold in the lexical frame too.
func TestValidateWritePath_DenyListSurvivesSymlinkedCwd(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "cwdlink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	opts := WritePathOptions{Cwd: NewCwdRef(link), DenyExact: DefaultDenyPaths(link)}
	target := filepath.Join(link, ".yottacode", "permissions.json")
	if err := ValidateWritePath(target, opts); err == nil {
		t.Fatal("write to permissions.json via a symlinked cwd was allowed (self-grant)")
	}
}

// TestValidateReadPath_RejectsSymlinkToDenyPath is a regression for the
// release audit's read deny-list symlink bypass: read_file/read_many_files
// open() FOLLOWS symlinks, so an innocuously-named symlink to a credential
// store must be blocked by the resolved real path, not just the literal.
func TestValidateReadPath_RejectsSymlinkToDenyPath(t *testing.T) {
	cwd := t.TempDir()
	secret := filepath.Join(cwd, "secret.key")
	if err := os.WriteFile(secret, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	deny := []string{secret}

	link := filepath.Join(cwd, "notes.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := ValidateReadPath(link, deny); err == nil {
		t.Fatalf("read via symlink to a deny path was allowed")
	}

	// A benign read still passes.
	plain := filepath.Join(cwd, "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadPath(plain, deny); err != nil {
		t.Errorf("benign read rejected: %v", err)
	}
}

// TestGrepDenyExcludes covers the recursion guard: a recursive grep must
// not descend INTO a credential store that lives under the search root.
// Regression for the read deny-list grep-root bypass.
func TestGrepDenyExcludes(t *testing.T) {
	root := filepath.FromSlash("/home/me/project")
	deny := []string{
		filepath.FromSlash("/home/me/project/.env"), // under root → excluded
		filepath.FromSlash("/home/me/.ssh"),         // not under root → ignored
	}
	rg, gr := grepDenyExcludes(root, deny)

	if !slices.Contains(rg, "!.env") {
		t.Errorf("rg excludes missing the in-root .env: %v", rg)
	}
	for _, a := range rg {
		if a == "!.ssh" || a == "!.ssh/**" {
			t.Errorf("rg should not exclude the out-of-root .ssh: %v", rg)
		}
	}
	if !slices.Contains(gr, "--exclude=.env") || !slices.Contains(gr, "--exclude-dir=.env") {
		t.Errorf("grep excludes missing the in-root .env: %v", gr)
	}
}
