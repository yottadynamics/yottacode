package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ModelsFile is the on-disk shape of a successful model catalog fetch.
// Sibling of TokenSet — same auth/ directory, same 0600 mode, same
// atomic write template. Wrapping the slice (rather than emitting
// bare `[]string`) leaves room for future fields like context windows
// or capability flags without breaking older binaries.
//
// Presence of this file on disk implies "a real fetch succeeded at
// ScannedAt"; failed fetches must NOT write the file (the previous
// file, if any, stays as the last-known-good list).
//
// DisplayNames maps a Models slug to the label the live catalog
// reported for it (ModelsFile written by an older binary may lack an
// entry for a given slug; readers fall back to the bare slug).
type ModelsFile struct {
	ScannedAt    time.Time         `json:"scanned_at"`
	Models       []string          `json:"models"`
	DisplayNames map[string]string `json:"display_names,omitempty"`
}

// ErrModelsNotFound is returned by LoadModels when no models file
// exists at the resolved path. Callers branch on this to distinguish
// "user has not scanned yet" from "I/O error".
var ErrModelsNotFound = errors.New("openai-auth: no models file")

// DefaultModelsPath returns ~/.yottacode/auth/openai-auth-models.json.
// Sibling of DefaultStorePath — both files live in the auth/ directory
// already covered by the agent's deny-paths.
func DefaultModelsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "auth", "openai-auth-models.json"), nil
}

// LoadModels reads the models file at path. ErrModelsNotFound when
// missing; any other I/O or decode error propagates verbatim.
func LoadModels(path string) (ModelsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ModelsFile{}, ErrModelsNotFound
		}
		return ModelsFile{}, err
	}
	var mf ModelsFile
	if err := json.Unmarshal(b, &mf); err != nil {
		return ModelsFile{}, fmt.Errorf("openai-auth: decode %s: %w", path, err)
	}
	return mf, nil
}

// SaveModels writes mf to path with mode 0600 atomically (tempfile +
// rename), creating parent dirs (0700) as needed. Mirrors Save() in
// store.go — same atomicity story, same permission story.
func SaveModels(path string, mf ModelsFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".openai-auth-models-*.tmp")
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
