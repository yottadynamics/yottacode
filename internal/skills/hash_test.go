package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHashInstalledDir_Stable verifies the hash is byte-for-byte
// stable across two invocations on identical content — the property
// `skills check` and `skills update` rely on for drift detection.
func TestHashInstalledDir_Stable(t *testing.T) {
	dir := seedSkill(t, "alpha")
	h1, err := HashInstalledDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashInstalledDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hash changed across calls: %s vs %s", h1, h2)
	}
}

// TestHashInstalledDir_DetectsContentChange flips one byte of the
// SKILL.md and confirms the top hash moves. Without this, drift
// detection silently passes for any edit that leaves file structure
// intact.
func TestHashInstalledDir_DetectsContentChange(t *testing.T) {
	dir := seedSkill(t, "alpha")
	h1, err := HashInstalledDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-2] ^= 0x01 // flip a bit
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := HashInstalledDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("hash did not change on content modification")
	}
}

// TestHashInstalledDir_DetectsPathChange renames a file in place.
// The byte contents are identical but the path moved, so the hash
// must move too — otherwise scripts/x.sh and scripts/y.sh would
// look interchangeable to drift detection.
func TestHashInstalledDir_DetectsPathChange(t *testing.T) {
	dir := seedSkill(t, "alpha")
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "first.sh"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	h1, _ := HashInstalledDir(dir)
	if err := os.Rename(filepath.Join(scripts, "first.sh"), filepath.Join(scripts, "second.sh")); err != nil {
		t.Fatal(err)
	}
	h2, _ := HashInstalledDir(dir)
	if h1 == h2 {
		t.Error("hash should change when a file is renamed")
	}
}

// TestHashInstalledDir_IgnoresLockfile guards against a future
// refactor that places the lockfile alongside per-skill dirs.
// Lockfile presence/content must never affect the per-skill hash.
func TestHashInstalledDir_IgnoresLockfile(t *testing.T) {
	dir := seedSkill(t, "alpha")
	h1, _ := HashInstalledDir(dir)
	if err := os.WriteFile(filepath.Join(dir, LockfileName), []byte(`{"junk":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, _ := HashInstalledDir(dir)
	if h1 != h2 {
		t.Error("lockfile presence changed the hash")
	}
}

// seedSkill writes a minimal valid SKILL.md to a fresh tempdir and
// returns the path. Used as a tiny shared fixture across the hash
// tests.
func seedSkill(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	body := "---\nname: " + name + "\ndescription: hash test fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
