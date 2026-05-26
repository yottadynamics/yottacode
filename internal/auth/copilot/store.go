package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenSet is the on-disk shape of a successful device-code login.
// Stores the long-lived GitHub OAuth token. The short-lived Copilot
// API token is cached in memory by TokenSource, not persisted.
type TokenSet struct {
	GitHubToken string    `json:"github_token"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// ErrNotFound is returned by Load when no token file exists.
var ErrNotFound = errors.New("copilot: no token file")

// DefaultStorePath returns ~/.yottacode/auth/copilot.json.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "auth", "copilot.json"), nil
}

// Load reads the token file at path.
func Load(path string) (TokenSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TokenSet{}, ErrNotFound
		}
		return TokenSet{}, err
	}
	var ts TokenSet
	if err := json.Unmarshal(b, &ts); err != nil {
		return TokenSet{}, fmt.Errorf("copilot: decode %s: %w", path, err)
	}
	return ts, nil
}

// Save writes ts to path with mode 0600 atomically.
func Save(path string, ts TokenSet) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".copilot-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
