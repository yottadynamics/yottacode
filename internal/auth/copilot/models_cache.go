package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CachedModel is one entry in the persisted models file.
type CachedModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxOutput     int    `json:"max_output,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
}

// ModelsFile is the on-disk cache of available Copilot models.
type ModelsFile struct {
	CachedAt time.Time     `json:"cached_at"`
	Models   []CachedModel `json:"models"`
}

var ErrModelsNotFound = errors.New("copilot: no models file")

func DefaultModelsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "auth", "copilot-models.json"), nil
}

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
		return ModelsFile{}, fmt.Errorf("copilot: decode %s: %w", path, err)
	}
	return mf, nil
}

func SaveModels(path string, mf ModelsFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".copilot-models-*.tmp")
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
