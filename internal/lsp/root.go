package lsp

import (
	"os"
	"path/filepath"
)

var rootMarkers = map[string][]string{
	"go":         {"go.work", "go.mod"},
	"typescript": {"tsconfig.json", "jsconfig.json", "package.json"},
	"python":     {"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"},
	"rust":       {"Cargo.toml"},
}

// WorkspaceRoot walks upward from path and returns the closest project root for
// lang based on common build/config markers. Falling back to the file directory
// keeps single-file projects working when no marker exists.
func WorkspaceRoot(path string, lang Language, fallback string) string {
	start := path
	if info, err := os.Stat(start); err == nil && !info.IsDir() {
		start = filepath.Dir(start)
	}
	if start == "" {
		start = fallback
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}
	for dir := abs; dir != ""; dir = filepath.Dir(dir) {
		for _, marker := range rootMarkers[lang.ID] {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if fallback != "" {
		if absFallback, err := filepath.Abs(fallback); err == nil {
			return absFallback
		}
		return fallback
	}
	return abs
}
