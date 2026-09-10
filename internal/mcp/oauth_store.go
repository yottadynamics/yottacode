package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// Token persistence for auth = "oauth" servers. One file per server name —
// ~/.yottacode/mcp-auth/<name>.json, mode 0600 — so a leaked token from one
// server can't be confused with another's, and revoking one server's
// access (LogoutToken) can't touch any other. Mirrors internal/github's
// WriteTokenFile/RemoveTokenFile: atomic temp-file + rename, 0700 parent
// dir, missing-file is not an error.

// oauthAuthDir returns ~/.yottacode/mcp-auth.
func oauthAuthDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "mcp-auth"), nil
}

// oauthTokenPath returns the on-disk path for name's persisted token.
func oauthTokenPath(name string) (string, error) {
	dir, err := oauthAuthDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// LoadToken reads name's persisted token. Returns (nil, nil) — not an
// error — when no token has been saved yet, matching tokenFromFile's
// every-failure-mode-is-fall-through posture in internal/github/auth.go:
// a missing/corrupt token file just means "not logged in", handled by
// falling into the interactive OAuth flow.
func LoadToken(name string) (*oauth2.Token, error) {
	path, err := oauthTokenPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, nil
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, nil
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, nil
	}
	return &tok, nil
}

// SaveToken persists tok for name atomically (temp file + rename), mode
// 0600 on the file, 0700 on the parent dir. Called every time the SDK's
// oauth2.TokenSource hands back a token — initial exchange and every
// silent refresh — via persistingTokenSource, so a later session's
// LoadToken always sees the freshest access/refresh pair.
func SaveToken(name string, tok *oauth2.Token) error {
	if tok == nil {
		return errors.New("mcp oauth: SaveToken: nil token")
	}
	path, err := oauthTokenPath(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create mcp-auth dir: %w", err)
	}
	body, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// DeleteToken removes name's persisted token, if any. Returns whether a
// file existed pre-call so `/mcp logout` / `yottacode mcp logout` can
// render "logged out" vs "already logged out" distinctly. Idempotent —
// deleting an already-absent token is not an error.
func DeleteToken(name string) (existed bool, err error) {
	path, err := oauthTokenPath(name)
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat: %w", statErr)
	}
	if err := os.Remove(path); err != nil {
		return true, fmt.Errorf("remove: %w", err)
	}
	return true, nil
}
