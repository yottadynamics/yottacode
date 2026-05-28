package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstall_WritesLockfile is the Phase 2 acceptance test for the
// install→lockfile bridge. Every install must produce a matching
// lockfile entry with hash, source, and a trust tag — or the rest
// of Phase 2 has nothing to read from.
func TestInstall_WritesLockfile(t *testing.T) {
	src, dest := stagedLocalSrc(t, "alpha")
	res, err := Install(InstallOptions{Source: src, DestRoot: dest})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	lf, err := LoadLockfile(dest)
	if err != nil {
		t.Fatalf("load lockfile: %v", err)
	}
	entry, ok := lf.Get("alpha")
	if !ok {
		t.Fatal("install did not record a lockfile entry")
	}
	if entry.Hash == "" {
		t.Error("lockfile entry missing hash")
	}
	if entry.Hash != res.Lock.Hash {
		t.Errorf("entry hash %q != install result hash %q", entry.Hash, res.Lock.Hash)
	}
	if entry.SourceType != SourceLocal {
		t.Errorf("source_type = %q", entry.SourceType)
	}
	if entry.Source != src {
		t.Errorf("source = %q, want %q", entry.Source, src)
	}
	if entry.Trust != TrustUnverified {
		t.Errorf("trust = %q, want %q", entry.Trust, TrustUnverified)
	}
}

// TestUninstall_DropsLockfileEntry locks the symmetric cleanup —
// without it, `skills check` would surface a "orphaned-lock" line
// for every removed skill until the user manually edited the file.
func TestUninstall_DropsLockfileEntry(t *testing.T) {
	src, dest := stagedLocalSrc(t, "alpha")
	t.Setenv("YOTTACODE_HOME", filepath.Dir(dest))
	// Make sure UserSkillsDir() resolves to `dest` for the Uninstall
	// call. YOTTACODE_HOME points one level up; the skills subdir is
	// `dest`'s basename, so stamp it that way.
	if filepath.Base(dest) != "skills" {
		// Use Setenv with a fresh wrap to make YOTTACODE_HOME/skills
		// resolve to dest.
		wrap := t.TempDir()
		t.Setenv("YOTTACODE_HOME", wrap)
		dest = filepath.Join(wrap, "skills")
	}
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall("alpha"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	lf, err := LoadLockfile(dest)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := lf.Get("alpha"); ok {
		t.Error("uninstall did not remove the lockfile entry")
	}
}

// TestCheck_StatusMatrix exercises every CheckStatus branch with a
// single Check() call against a destRoot that contains: a clean
// install, a user-modified install, an orphaned-lock entry (lock
// without dir), and a missing-lock dir (dir without lock). Catches
// regressions in the status-selection logic — easy to misclassify
// without a matrix test.
func TestCheck_StatusMatrix(t *testing.T) {
	dest := t.TempDir()

	// Clean: install normally, leave it alone.
	srcClean, _ := stagedLocalSrcAt(t, "clean", dest)
	if _, err := Install(InstallOptions{Source: srcClean, DestRoot: dest}); err != nil {
		t.Fatal(err)
	}

	// Modified: install, then edit the body in place.
	srcModified, _ := stagedLocalSrcAt(t, "modified", dest)
	if _, err := Install(InstallOptions{Source: srcModified, DestRoot: dest}); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(dest, "modified", "SKILL.md")
	body, _ := os.ReadFile(skillFile)
	body = append(body, '\n', '#', ' ', 'e', 'd', 'i', 't', '\n')
	if err := os.WriteFile(skillFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	// Orphan: install, then nuke the dir but leave the lockfile entry.
	srcOrphan, _ := stagedLocalSrcAt(t, "orphan", dest)
	if _, err := Install(InstallOptions{Source: srcOrphan, DestRoot: dest}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dest, "orphan")); err != nil {
		t.Fatal(err)
	}

	// Missing-lock: drop a skill dir directly, no install call so no
	// lockfile entry exists.
	noLockDir := filepath.Join(dest, "nolock")
	if err := os.MkdirAll(noLockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body = []byte("---\nname: nolock\ndescription: missing lock fixture\n---\nBody\n")
	if err := os.WriteFile(filepath.Join(noLockDir, "SKILL.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Check(CheckOptions{DestRoot: dest})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	byName := map[string]CheckStatus{}
	for _, r := range results {
		byName[r.Name] = r.Status
	}
	want := map[string]CheckStatus{
		"clean":    CheckOK,
		"modified": CheckModified,
		"orphan":   CheckOrphanedLock,
		"nolock":   CheckMissingLock,
	}
	for name, status := range want {
		if got := byName[name]; got != status {
			t.Errorf("%s: got %q, want %q", name, got, status)
		}
	}
}

// TestUpdate_HappyPath edits the upstream source between install and
// update; the second hash must differ from the first and the on-disk
// bytes must reflect the new source. This is the core Phase 2 user
// story: "fetch the latest version of remote-ops."
func TestUpdate_HappyPath(t *testing.T) {
	src, dest := stagedLocalSrc(t, "alpha")
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err != nil {
		t.Fatal(err)
	}
	// Edit the upstream (source) to simulate an upstream release.
	upstream := filepath.Join(src, "SKILL.md")
	body, _ := os.ReadFile(upstream)
	body = append(body, '\n', 'u', 'p', 'd', 'a', 't', 'e', 'd', '\n')
	if err := os.WriteFile(upstream, body, 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Update(UpdateOptions{DestRoot: dest})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Status != UpdateUpdated {
		t.Errorf("status = %q, want %q", results[0].Status, UpdateUpdated)
	}
	if results[0].OldHash == results[0].NewHash {
		t.Errorf("old == new hash; update should have changed bytes")
	}
}

// TestUpdate_SkipsUserModified is the safety-net test for the
// dirty-detection rule. A hand-edited install must not be overwritten
// without --force, even when the upstream has changed.
func TestUpdate_SkipsUserModified(t *testing.T) {
	src, dest := stagedLocalSrc(t, "alpha")
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err != nil {
		t.Fatal(err)
	}
	// Edit the *installed* copy — this is the "user-modified" state.
	installed := filepath.Join(dest, "alpha", "SKILL.md")
	body, _ := os.ReadFile(installed)
	body = append(body, '\n', 'h', 'a', 'n', 'd', 'e', 'd', 'i', 't', '\n')
	if err := os.WriteFile(installed, body, 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Update(UpdateOptions{DestRoot: dest})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if results[0].Status != UpdateSkippedDirty {
		t.Errorf("status = %q, want %q", results[0].Status, UpdateSkippedDirty)
	}
	// Hand-edited body must still be there.
	got, _ := os.ReadFile(installed)
	if string(got) != string(body) {
		t.Error("dirty skip should have left the hand-edit intact")
	}

	// --force overrides and refetches.
	results, err = Update(UpdateOptions{DestRoot: dest, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != UpdateUnchanged && results[0].Status != UpdateUpdated {
		t.Errorf("force update status = %q, want updated or unchanged", results[0].Status)
	}
	got, _ = os.ReadFile(installed)
	if string(got) == string(body) {
		t.Error("--force should have replaced the hand-edit")
	}
}

// TestUpdate_UnknownNameErrors keeps the error message accurate for
// the CLI surface — without this, `yottacode skills update foo` for
// an unknown skill would either no-op or fail with a confusing
// internal error.
func TestUpdate_UnknownNameErrors(t *testing.T) {
	if _, err := Update(UpdateOptions{DestRoot: t.TempDir(), Name: "nope"}); err == nil {
		t.Error("update of an unknown name should error")
	}
}

// stagedLocalSrc creates a (sourceDir, destRoot) pair for tests:
// sourceDir holds a minimal valid SKILL.md whose name is the
// supplied slug; destRoot is an empty install root. Returned paths
// are absolute t-tempdirs.
func stagedLocalSrc(t *testing.T, slug string) (string, string) {
	t.Helper()
	src := t.TempDir()
	body := "---\nname: " + slug + "\ndescription: update test fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return src, t.TempDir()
}

// stagedLocalSrcAt is stagedLocalSrc against a caller-supplied
// destRoot so the matrix test can install multiple skills into the
// same dest.
func stagedLocalSrcAt(t *testing.T, slug, dest string) (string, string) {
	t.Helper()
	src := t.TempDir()
	body := "---\nname: " + slug + "\ndescription: update test fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return src, dest
}
