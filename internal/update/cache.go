package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cacheTTL = 24 * time.Hour

type cacheRecord struct {
	LastChecked   time.Time `json:"last_checked"`
	LatestVersion string    `json:"latest_version"`
	ReleaseURL    string    `json:"release_url"`
}

func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "cache", "update-check.json"), nil
}

// readCache returns the cached record, whether it is fresh, and whether it is
// usable. Stale records remain usable for immediate display while the caller
// revalidates them; missing, corrupt, empty, or future-dated records are not.
// Errors are intentionally swallowed — the update path must never fail loudly.
func readCache(now time.Time) (cacheRecord, bool, bool) {
	path, err := cachePath()
	if err != nil {
		return cacheRecord{}, false, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheRecord{}, false, false
	}
	var rec cacheRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return cacheRecord{}, false, false
	}
	age := now.Sub(rec.LastChecked)
	if age < 0 || strings.TrimSpace(rec.LatestVersion) == "" {
		return cacheRecord{}, false, false
	}
	return rec, age <= cacheTTL, true
}

func writeCache(rec cacheRecord) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	// A same-directory uniquely named temporary file plus rename prevents
	// concurrent processes or interrupted writes from exposing partial JSON.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
