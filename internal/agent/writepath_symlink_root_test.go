package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These reproduce, on ANY platform, the symlinked-workspace-root condition
// that arises implicitly on macOS — where t.TempDir() lives under
// /var/folders -> /private/var and /etc -> /private/etc. They are
// regressions for the macOS-only CI failures of
// TestValidateReadPath_RejectsSymlinkToDenyPath and
// TestValidateWritePath_RejectsOutsideCwd: the deny/containment frames were
// mismatched (resolved candidate vs lexical deny; resolved vs lexical error
// path). Building an explicit symlinked root exercises the same path on Linux
// so the gap can't hide on a runner we don't watch.

// TestValidateReadPath_DenyMatchesAcrossSymlinkedRoot reaches the workspace
// through a symlinked parent, so a deny-pointing symlink resolves to a real
// path in a different frame than the (lexical) deny entry — the exact macOS
// gap. The read must still be blocked.
func TestValidateReadPath_DenyMatchesAcrossSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	// cwd is a symlink to real, mimicking macOS /var -> /private/var.
	cwd := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(real, cwd); err != nil {
		t.Fatalf("symlink cwd: %v", err)
	}
	secret := filepath.Join(cwd, "secret.key")
	if err := os.WriteFile(secret, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	deny := []string{secret} // lexical, in the symlinked-cwd frame

	link := filepath.Join(cwd, "notes.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink notes: %v", err)
	}
	if err := ValidateReadPath(link, deny); err == nil {
		t.Fatal("read via a symlink to a deny path across a symlinked root was allowed")
	}

	// Benign read under the same symlinked root still passes (no false positive).
	plain := filepath.Join(cwd, "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadPath(plain, deny); err != nil {
		t.Errorf("benign read rejected: %v", err)
	}
}

// TestValidateWritePath_ErrorReportsLexicalPath checks that a rejected write
// whose parent is a symlink surfaces the path the caller specified, not the
// symlink-resolved one (the macOS /etc -> /private/etc surprise that made the
// sentinel report /private/etc/passwd).
func TestValidateWritePath_ErrorReportsLexicalPath(t *testing.T) {
	cwd := t.TempDir()
	outsideReal := t.TempDir()
	// A symlinked parent dir outside cwd, mimicking macOS /etc -> /private/etc.
	linkParent := filepath.Join(t.TempDir(), "etclink")
	if err := os.Symlink(outsideReal, linkParent); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	target := filepath.Join(linkParent, "passwd") // the lexical path the caller gives

	err := ValidateWritePath(target, WritePathOptions{Cwd: NewCwdRef(cwd)})
	var sentinel *ErrPathOutsideWorkspace
	if !errors.As(err, &sentinel) {
		t.Fatalf("expected *ErrPathOutsideWorkspace, got %v", err)
	}
	if sentinel.Path != target {
		t.Errorf("sentinel.Path = %q, want %q (lexical, not symlink-resolved)", sentinel.Path, target)
	}
}
