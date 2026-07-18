package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Model access scans are scoped to one Vertex provider kind plus one
// project/location base URL. Vertex's public model catalog says which
// models exist, but access is granted per GCP project and location, so
// the picker needs a local cache of what this project can actually call.
const (
	ProviderVertex          = "vertex"
	ProviderVertexAnthropic = "vertex-anthropic"
)

// ModelsFile is the on-disk shape of a successful Vertex access scan.
// Models is the OK-only list that pickers use; Results preserves the
// rejected rows so users can audit why a model is greyed out.
type ModelsFile struct {
	ScannedAt  time.Time    `json:"scanned_at"`
	Provider   string       `json:"provider"`
	BaseURL    string       `json:"base_url"`
	Project    string       `json:"project,omitempty"`
	Location   string       `json:"location,omitempty"`
	Candidates []string     `json:"candidates"`
	Models     []string     `json:"models"`
	Results    []ScanResult `json:"results,omitempty"`
}

// ErrModelsNotFound is returned when no scan cache exists for a provider
// base URL. Missing means access is unknown, not unavailable.
var ErrModelsNotFound = errors.New("vertex: no models access file")

// DefaultModelsPath returns the per-project/location model-access cache
// path under ~/.yottacode/auth/vertex-models/.
func DefaultModelsPath(provider, baseURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	project, location := ProjectLocation(baseURL)
	if project == "" {
		project = "unknown-project"
	}
	if location == "" {
		location = "unknown-location"
	}
	return filepath.Join(home, ".yottacode", "auth", "vertex-models", safePathPart(project), safePathPart(location), safePathPart(provider)+".json"), nil
}

// ProjectLocation extracts the /projects/<p>/locations/<l> pair from a
// Vertex base_url. It tolerates extra suffixes such as /endpoints/openapi.
func ProjectLocation(raw string) (project, location string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "projects":
			project = parts[i+1]
		case "locations":
			location = parts[i+1]
		}
	}
	return project, location
}

var unsafePathPart = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safePathPart(s string) string {
	s = unsafePathPart.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.Trim(s, "._-")
	if s == "" {
		return "unknown"
	}
	return s
}

// LoadModels reads a Vertex access cache. ErrModelsNotFound means the
// user has not scanned this project/location yet.
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
		return ModelsFile{}, fmt.Errorf("vertex: decode %s: %w", path, err)
	}
	return mf, nil
}

// SaveModels writes a Vertex access cache atomically with private file
// permissions; the file records project model access and may include
// provider error text.
func SaveModels(path string, mf ModelsFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".vertex-models-*.tmp")
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

// AccessMap loads the last scan and returns a model id -> available map
// containing only models with a *definitive* verdict: reachable models
// map to true, models Vertex explicitly denied (403/404) map to false.
// Models whose probe was inconclusive (network error, cancellation, 5xx)
// are omitted, so the picker treats them as not-yet-scanned rather than
// denied. The second return is false only when no scan file exists at
// all, letting callers distinguish "never scanned" from "scanned, access
// known".
func AccessMap(provider, baseURL string) (map[string]bool, bool) {
	path, err := DefaultModelsPath(provider, baseURL)
	if err != nil {
		return nil, false
	}
	mf, err := LoadModels(path)
	if err != nil {
		return nil, false
	}
	out := make(map[string]bool, len(mf.Results))
	for _, r := range mf.Results {
		switch r.EffectiveAccess() {
		case AccessAvailable:
			out[r.Name] = true
		case AccessDenied:
			out[r.Name] = false
		// AccessUnknown is deliberately omitted: the picker treats a
		// missing entry as "not scanned" and leaves the model enabled, so
		// a transient scan failure never disables a usable model.
		}
	}
	return out, true
}

// OKNames returns successful model ids in scan order.
func OKNames(results []ScanResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.OK {
			out = append(out, r.Name)
		}
	}
	return out
}

// PersistScan writes the result of a completed scan to the cache path for
// provider/baseURL. It writes even when zero models are available, because
// that is useful evidence for the picker and for debugging IAM/location
// mistakes.
func PersistScan(provider, baseURL string, candidates []string, results []ScanResult) (ModelsFile, string, error) {
	project, location := ProjectLocation(baseURL)
	mf := ModelsFile{
		ScannedAt:  time.Now().UTC(),
		Provider:   provider,
		BaseURL:    strings.TrimSpace(baseURL),
		Project:    project,
		Location:   location,
		Candidates: append([]string(nil), candidates...),
		Models:     OKNames(results),
		Results:    append([]ScanResult(nil), results...),
	}
	path, err := DefaultModelsPath(provider, baseURL)
	if err != nil {
		return ModelsFile{}, "", err
	}
	if err := SaveModels(path, mf); err != nil {
		return ModelsFile{}, path, err
	}
	return mf, path, nil
}

// TokenSource is the token seam used by the scanner. *TokenSource from
// source.go satisfies it, while tests can inject static tokens.
type ScannerTokenSource interface {
	Token(context.Context) (string, error)
}
