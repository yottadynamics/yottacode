package hashline

import (
	"os"
	"path/filepath"
)

// ApplyFile reads path, applies hunks, and replaces the file with a same-directory
// temp file rename. The same directory keeps the rename on the same filesystem,
// which gives POSIX platforms atomic replacement semantics for readers.
func ApplyFile(path string, hunks []Hunk) error {
	src, err := os.ReadFile(path)
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
	return writeAtomic(path, out, info.Mode().Perm())
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
