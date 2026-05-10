package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenSet is the on-disk shape of a successful login. Refresh writes
// a new TokenSet over the same file. JSON (not .env) because tokens
// rotate on refresh and rewriting .env in place would clobber user
// comments and race with shell-loaded values.
type TokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id,omitempty"`
	Email        string    `json:"email,omitempty"`
}

// ErrNotFound is returned by Load when no token file exists at the
// resolved path. Callers branch on this to distinguish "user has not
// logged in yet" from "I/O error".
var ErrNotFound = errors.New("openai-auth: no token file")

// DefaultStorePath returns ~/.yottacode/auth/openai-auth.json. The
// auth/ subdirectory is yottacode-managed: the wizard does not write
// here, only the OAuth flow does. Mode 0700 on the dir, 0600 on the
// file.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "auth", "openai-auth.json"), nil
}

// Load reads the token file at path. ErrNotFound when missing; any
// other I/O or decode error propagates verbatim so the caller can
// log / surface it.
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
		return TokenSet{}, fmt.Errorf("openai-auth: decode %s: %w", path, err)
	}
	return ts, nil
}

// Save writes ts to path with mode 0600 atomically (tempfile +
// rename), creating parent dirs (0700) as needed. Atomicity matters
// because a partial write during refresh could leave an empty file
// shadowing a previously valid token, forcing a re-login.
func Save(path string, ts TokenSet) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".openai-auth-*.tmp")
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
