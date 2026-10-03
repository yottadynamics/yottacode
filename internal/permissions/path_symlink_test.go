package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluate_PathRulesFollowSymlinkTargets(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"), nil, nil, []string{"Read(" + filepath.ToSlash(outside) + "/**)"})
	p, err := LoadWithSystemPath(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join("linked", "secret.txt")})
	if got := p.Evaluate("read_file", string(args)); got != Deny {
		t.Fatalf("symlink target deny = %v, want Deny", got)
	}
}

func TestRelPath_CanonicalizesSymlinkEscape(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(cwd, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	got := relPath(filepath.Join("linked", "new.txt"), cwd)
	want := filepath.ToSlash(filepath.Join(outside, "new.txt"))
	if got != want {
		t.Fatalf("relPath symlink escape = %q, want %q", got, want)
	}
}
