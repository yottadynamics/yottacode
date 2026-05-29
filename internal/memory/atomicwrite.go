package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// ArchiveDirName is the subdirectory inside a memory directory where
// prior versions of overwritten memories are kept. It is a directory, so
// scanMemoryDir skips it — archived versions never appear in the index,
// retrieval, or `memory list`; they exist only for recovery.
const ArchiveDirName = ".archive"

// ArchivePrior copies the current contents of memPath (if the file
// exists) into <dir>/.archive/<name>.<stamp>.md so an overwrite can
// never silently destroy a different memory that happened to share the
// name. stamp must be unique per call (the caller passes a timestamp).
// Returns the archive path written, or "" when there was nothing to
// archive (the file did not exist). Durable like every other memory
// write (staged + fsync'd via AtomicWrite).
func ArchivePrior(memPath, stamp string) (string, error) {
	data, err := os.ReadFile(memPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(memPath), ArchiveDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(memPath), ".md")
	// os.CreateTemp guarantees a unique filename even when two archivers
	// race with the same stamp (UnixNano is not collision-proof across
	// goroutines/processes, and clock granularity can be coarse) — so a
	// second concurrent archive can never clobber the first. The stamp
	// stays in the name for human readability; the random suffix from the
	// "*" makes it unique.
	f, err := os.CreateTemp(dir, base+"."+stamp+".*.md")
	if err != nil {
		return "", err
	}
	archivePath := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(archivePath)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(archivePath)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return archivePath, nil
}

// AtomicWrite writes data to path durably and atomically.
//
// It stages to a UNIQUE temp file in the same directory (via os.CreateTemp),
// fsyncs the data, renames onto the destination, then fsyncs the directory
// so the rename survives a crash. The temp file is removed on every error
// path; a successful rename consumes it, making the deferred Remove a no-op.
//
// This replaces the older "<path>.tmp" + os.WriteFile + os.Rename pattern,
// which had two latent defects the memory layer relied on:
//
//   - A DETERMINISTIC temp name ("<path>.tmp"): two writers targeting the
//     same destination (two yottacode processes in one repo, or a parent loop
//     and a detached background subagent sharing the same tool) staged through
//     the same file, so their bytes interleaved into one descriptor or one
//     rename fired mid-write — corrupting the destination. A unique temp name
//     makes concurrent writes last-writer-wins on a valid file instead.
//   - No fsync before rename: on a crash/power-loss just after the call
//     returned "saved", the file could come back zero-length or stale.
//
// Using os.CreateTemp also closes a staging-file symlink-follow gap: it
// refuses to open through a pre-planted symlink at the temp path.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// No-op once a successful rename consumes tmp; cleans up every error path.
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir best-effort fsyncs a directory so a rename into it is durable.
// Failures are ignored: not every platform/filesystem supports directory
// fsync, and the rename itself has already happened.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
