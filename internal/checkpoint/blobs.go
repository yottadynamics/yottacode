package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// writeBlob stores bytes under blobs/<sha>.blob using the .tmp + rename
// atomic pattern. No-op when the blob already exists — sha collision is
// taken as proof of identical content.
func (s *Store) writeBlob(sessionID, sha string, bytes []byte) error {
	if sha == "" {
		return errors.New("checkpoint: writeBlob requires non-empty sha")
	}
	dir := s.blobsDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, sha+".blob")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return fmt.Errorf("checkpoint: write blob: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("checkpoint: rename blob: %w", err)
	}
	return nil
}

// readBlob loads the bytes stored at blobs/<sha>.blob.
func (s *Store) readBlob(sessionID, sha string) ([]byte, error) {
	if sha == "" {
		return nil, errors.New("checkpoint: readBlob requires non-empty sha")
	}
	return os.ReadFile(filepath.Join(s.blobsDir(sessionID), sha+".blob"))
}
