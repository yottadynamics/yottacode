package hashline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileForEditLimited_RejectsFileOverLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "f.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readFileForEditLimited(path, 9)
	if !errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestReadFileForEditLimited_AllowsFileExactlyAtLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "f.txt")
	content := []byte("0123456789")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFileForEditLimited(path, int64(len(content)))
	if err != nil {
		t.Fatalf("a file exactly at the limit must be allowed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

// A missing file must fail as a plain not-found error, not get misreported
// as file_too_large (the size check stats before it can know the file even
// exists to compare).
func TestReadFileForEditLimited_PropagatesMissingFileError(t *testing.T) {
	_, err := readFileForEditLimited(filepath.Join(t.TempDir(), "nope.txt"), 100)
	if err == nil {
		t.Fatal("want an error for a missing file")
	}
	if errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want a not-found error, not file_too_large", err)
	}
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want it to unwrap to a not-exist error", err)
	}
}

// The oversized file's content is never read: the check must reject on stat
// alone. Exercised by truncating (sparse) to a size larger than what this
// process could plausibly read in a test's timeout if it actually tried.
func TestReadFileForEditLimited_RejectsWithoutReadingContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const hugeSize = 1 << 40 // 1 TiB, sparse — costs no real disk or memory
	if err := f.Truncate(hugeSize); err != nil {
		f.Close()
		t.Skipf("sparse file not supported on this filesystem: %v", err)
	}
	f.Close()

	_, err = readFileForEditLimited(path, MaxEditFileBytes)
	if !errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

// mustSparseFile creates a file that reports the given size via stat without
// writing (or later reading) that many real bytes, so boundary tests around
// MaxEditFileBytes stay fast.
func mustSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Skipf("sparse file not supported on this filesystem: %v", err)
	}
}

// ApplyFile must reject an oversized target before reading it, not just
// HashSpan/Apply on bytes the caller already loaded.
func TestApplyFile_RejectsFileOverLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.bin")
	mustSparseFile(t, path, MaxEditFileBytes+1)

	err := ApplyFile(path, []Hunk{{Anchor: Anchor{Hash: hashBytes([]byte("x"))}, Old: []byte("x"), New: []byte("y")}})
	if !errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

// ReplaceFileIfUnchanged re-reads the target under its file lock to detect a
// concurrent change; if the file has grown past the cap by that point, the
// re-read must not silently load it all just to report "changed" — it should
// report why in a way that identifies the actual problem.
func TestReplaceFileIfUnchanged_RejectsWhenTargetGrowsPastLimitBeforeReread(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.bin")
	mustSparseFile(t, path, MaxEditFileBytes+1)

	err := ReplaceFileIfUnchanged(path, []byte("whatever the caller last read"), []byte("new"))
	if !errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge, not a generic/concurrent-write error", err)
	}
}

// Pins the wired-up public constant and function together, not just the
// parameterized logic above.
func TestReadFileForEdit_RealConstantBoundary(t *testing.T) {
	tmp := t.TempDir()
	atLimit := filepath.Join(tmp, "at-limit.bin")
	overLimit := filepath.Join(tmp, "over-limit.bin")
	mustSparseFile(t, atLimit, MaxEditFileBytes)
	mustSparseFile(t, overLimit, MaxEditFileBytes+1)

	got, err := ReadFileForEdit(atLimit)
	if err != nil {
		t.Fatalf("a file exactly at MaxEditFileBytes must be allowed: %v", err)
	}
	if int64(len(got)) != MaxEditFileBytes {
		t.Fatalf("read %d bytes, want %d", len(got), MaxEditFileBytes)
	}

	_, err = ReadFileForEdit(overLimit)
	if !errorKindIs(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge for one byte over the limit", err)
	}
}
