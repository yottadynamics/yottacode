package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// helperFixture builds <tmp>/leakless-x/leakless with the given modes.
func helperFixture(t *testing.T, dirMode, fileMode os.FileMode) (dir, bin string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "leakless-x")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "leakless")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod explicitly: WriteFile/Mkdir modes are masked by the umask.
	if err := os.Chmod(bin, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

func TestVerifyLeaklessHelper_OwnHelperPasses(t *testing.T) {
	_, bin := helperFixture(t, 0o700, 0o755)
	if err := verifyLeaklessHelper(bin, os.Getuid()); err != nil {
		t.Fatalf("a private helper owned by us should pass: %v", err)
	}
}

// rod creates the directory 0775. That's ours, so it is tightened rather than
// rejected — and it must be tightened BEFORE the file is trusted, so nobody
// with group/other write can swap it afterwards.
func TestVerifyLeaklessHelper_LoosePermissionsOnOurDirAreTightened(t *testing.T) {
	dir, bin := helperFixture(t, 0o775, 0o755)
	if err := verifyLeaklessHelper(bin, os.Getuid()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 0700 after verification", got)
	}
}

// The attack: someone else pre-creates the predictable path. Simulated by
// asking for a different current uid, since a test can't chown to another user.
func TestVerifyLeaklessHelper_RejectsAnotherUsersHelper(t *testing.T) {
	_, bin := helperFixture(t, 0o700, 0o755)
	if err := verifyLeaklessHelper(bin, os.Getuid()+1); err == nil {
		t.Fatal("a helper owned by a different user must be rejected")
	}
}

func TestVerifyLeaklessHelper_RejectsWritableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o757, 0o777} {
		_, bin := helperFixture(t, 0o700, mode)
		if err := verifyLeaklessHelper(bin, os.Getuid()); err == nil {
			t.Errorf("a helper with mode %o (writable by others) must be rejected", mode)
		}
	}
}

func TestVerifyLeaklessHelper_RejectsSymlinks(t *testing.T) {
	dir, bin := helperFixture(t, 0o700, 0o755)

	// symlinked file
	link := filepath.Join(dir, "leakless-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaklessHelper(link, os.Getuid()); err == nil {
		t.Error("a symlinked helper file must be rejected")
	}

	// symlinked directory
	dirLink := filepath.Join(t.TempDir(), "leakless-dir-link")
	if err := os.Symlink(dir, dirLink); err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaklessHelper(filepath.Join(dirLink, "leakless"), os.Getuid()); err == nil {
		t.Error("a symlinked helper directory must be rejected")
	}
}

func TestVerifyLeaklessHelper_MissingAndNonRegular(t *testing.T) {
	dir, _ := helperFixture(t, 0o700, 0o755)
	if err := verifyLeaklessHelper(filepath.Join(dir, "nope"), os.Getuid()); err == nil {
		t.Error("a missing helper must be rejected")
	}
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaklessHelper(sub, os.Getuid()); err == nil {
		t.Error("a directory where the helper should be must be rejected")
	}
}

// leaklessUsable decides whether the browser launches under the guard at all.
func TestLeaklessUsableWith(t *testing.T) {
	_, good := helperFixture(t, 0o700, 0o755)
	uid := os.Getuid()

	if !leaklessUsableWith(func() bool { return true }, func() string { return good }, uid) {
		t.Error("a verified helper should be usable")
	}
	if leaklessUsableWith(func() bool { return false }, func() string { return good }, uid) {
		t.Error("an unsupported platform must not report usable")
	}
	if leaklessUsableWith(func() bool { return true }, func() string { return good }, uid+1) {
		t.Error("a helper owned by someone else must not be usable")
	}
	// GetLeaklessBin panics on I/O errors; that must degrade, not crash the launch.
	if leaklessUsableWith(func() bool { return true }, func() string { panic("read-only /tmp") }, uid) {
		t.Error("a panicking helper lookup must report not usable")
	}
}

// The scenario the directory check can't cover: a directory that IS ours (or is
// open to group members) but with a helper somebody else planted inside it. Only
// the file's own ownership check stops that, so it is tested on its own — the
// directory reports as ours, the file as another user's.
func TestVerifyLeaklessHelper_RejectsFilePlantedInOurDirectory(t *testing.T) {
	_, bin := helperFixture(t, 0o700, 0o755)
	uid := os.Getuid()
	orig := ownerOf
	t.Cleanup(func() { ownerOf = orig })
	ownerOf = func(fi os.FileInfo) (int, bool) {
		if fi.IsDir() {
			return uid, true // the directory is ours...
		}
		return uid + 1, true // ...but the file inside is somebody else's
	}
	if err := verifyLeaklessHelper(bin, uid); err == nil {
		t.Fatal("a helper file owned by another user must be rejected even inside our own directory")
	}
}
