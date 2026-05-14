package update

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// readCache returns the cached record and a fresh flag. A miss (no file,
// parse error, expired, future-dated) returns fresh=false; the caller
// should refetch. Errors are intentionally swallowed — the update path
// must never fail loudly.
func readCache(now time.Time) (cacheRecord, bool) {
	path, err := cachePath()
	if err != nil {
		return cacheRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheRecord{}, false
	}
	var rec cacheRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return cacheRecord{}, false
	}
	age := now.Sub(rec.LastChecked)
	if age < 0 || age > cacheTTL {
		return rec, false
	}
	return rec, true
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
