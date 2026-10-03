package hashline

import (
	"bytes"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ApplyFile reads path, applies hunks, and replaces the file with a same-directory
// temp file rename. The same directory keeps the rename on the same filesystem,
// which gives POSIX platforms atomic replacement semantics for readers.
func ApplyFile(path string, hunks []Hunk) error {
	// Resolve once and use the resolved target for the entire compare-and-replace
	// sequence. Resolving after locking the original path would leave a TOCTOU
	// window where a symlink could be redirected between validation and rename.
	path = realTargetPath(path)
	src, err := ReadFileForEdit(path)
	if err != nil {
		return err
	}
	out, err := Apply(src, hunks)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeAtomicIfUnchanged(path, src, out, info.Mode().Perm())
}

// ReplaceFileIfUnchanged atomically replaces path only when it still contains expected.
func ReplaceFileIfUnchanged(path string, expected, content []byte) error {
	// Resolve once before opening the lock file. All subsequent operations use
	// this stable target path rather than following a mutable link again.
	path = realTargetPath(path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeAtomicIfUnchanged(path, expected, content, info.Mode().Perm())
}

func writeAtomicIfUnchanged(path string, expected, content []byte, mode os.FileMode) error {
	lockFile, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	// Re-read under the lock to detect a concurrent change since the caller's
	// own read. Capped for the same reason the caller's read is: the file
	// could have grown past the limit in between.
	current, err := ReadFileForEdit(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return &ApplyError{Kind: ErrConcurrentWrite, Message: "file changed while the edit was being prepared"}
	}
	return writeAtomic(path, content, mode)
}

// realTargetPath resolves path through any symlinks in its own name or its
// parent directories, so the caller can write to the real underlying file. It
// falls back to path unchanged if resolution fails, which should not happen
// here: every caller has just successfully read or stat'd path.
func realTargetPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keepTemp = true
	_ = syncDir(dir)
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
