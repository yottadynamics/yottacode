package hashline

import (
	"os"
	"path/filepath"
	"testing"
)

// These cover ReplaceFileIfUnchanged/ApplyFile writing through a symlinked
// path. The default write-path validator (internal/agent) rejects a
// symlinked leaf before hashline ever sees one, but AllowSymlinks exists as
// public API for a future caller to opt in, so the library must not corrupt
// a symlink on its own the moment something does.

func assertSymlinkPreserved(t *testing.T, link, wantTarget string) {
	t.Helper()
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", link, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink (mode=%v)", link, fi.Mode())
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink(%s): %v", link, err)
	}
	if got != wantTarget {
		t.Fatalf("symlink now points to %q, want %q", got, wantTarget)
	}
}

func TestReplaceFileIfUnchanged_WritesThroughSymlinkInSameDirectory(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFileIfUnchanged(link, []byte("old\n"), []byte("new\n")); err != nil {
		t.Fatalf("ReplaceFileIfUnchanged: %v", err)
	}

	assertSymlinkPreserved(t, link, "real.txt")
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatalf("ReadFile(real): %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("real file content = %q, want %q", got, "new\n")
	}
}

func TestReplaceFileIfUnchanged_WritesThroughSymlinkInDifferentDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target")
	linkDir := filepath.Join(root, "links")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(targetDir, "real.txt")
	link := filepath.Join(linkDir, "link.txt")
	if err := os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFileIfUnchanged(link, []byte("old\n"), []byte("new\n")); err != nil {
		t.Fatalf("ReplaceFileIfUnchanged: %v", err)
	}

	assertSymlinkPreserved(t, link, real)
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatalf("ReadFile(real): %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("real file content = %q, want %q", got, "new\n")
	}
	// No stray temp/regular file was left in the link's own directory.
	entries, err := os.ReadDir(linkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "link.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("link directory contents = %v, want just [link.txt]", names)
	}
}

func TestApplyFile_WritesThroughSymlinkAndPreservesIt(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(real, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}
	anchor := mustHashSpan(t, []byte("alpha\nbeta\n"), 6, 4)

	if err := ApplyFile(link, []Hunk{{Anchor: anchor, Old: []byte("beta"), New: []byte("BETA")}}); err != nil {
		t.Fatalf("ApplyFile: %v", err)
	}

	assertSymlinkPreserved(t, link, "real.txt")
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nBETA\n" {
		t.Fatalf("real file content = %q", got)
	}
}

// A concurrent modification of the real target (through the symlink or
// directly) must still be caught, exactly as for a plain file.
func TestReplaceFileIfUnchanged_DetectsConcurrentChangeThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}
	// expected no longer matches: someone else already changed the target.
	if err := os.WriteFile(real, []byte("changed-elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ReplaceFileIfUnchanged(link, []byte("old\n"), []byte("new\n"))
	if !errorKindIs(err, ErrConcurrentWrite) {
		t.Fatalf("err = %v, want ErrConcurrentWrite", err)
	}
	assertSymlinkPreserved(t, link, "real.txt")
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "changed-elsewhere\n" {
		t.Fatalf("real file content = %q, want it untouched by the rejected write", got)
	}
}

func TestReplaceFileIfUnchanged_PreservesModeThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	// A mode nothing else in this file uses, so a stray regular file created
	// at 0644 by the (broken) rename-over-the-link path cannot pass by luck.
	if err := os.WriteFile(real, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFileIfUnchanged(link, []byte("old\n"), []byte("new\n")); err != nil {
		t.Fatalf("ReplaceFileIfUnchanged: %v", err)
	}
	assertSymlinkPreserved(t, link, "real.txt")
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("real file content = %q, want the write to have landed", got)
	}
}

// A plain (non-symlink) path must behave exactly as before: this pins the
// existing atomic-write contract so the symlink-resolution change cannot
// quietly alter it.
func TestReplaceFileIfUnchanged_RegularFileUnaffected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFileIfUnchanged(path, []byte("old\n"), []byte("new\n")); err != nil {
		t.Fatalf("ReplaceFileIfUnchanged: %v", err)
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("a plain file must not become a symlink")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("content = %q", got)
	}
}
