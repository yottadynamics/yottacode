package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// catalogJSON is the generated catalog, embedded at build time. The
// file is rewritten by `go run ./cmd/yotta-models refresh`. An empty
// or missing-keys file is fine — Get returns an empty slice and the
// wizard/TUI fall back to free-text input.
//
//go:embed catalog.gen.json
var catalogJSON []byte

// File is the on-disk shape of catalog.gen.json. Keeping the wrapper
// (rather than a top-level array) gives us room to add metadata
// (refresh timestamp, schema version) without breaking older binaries.
type File struct {
	GeneratedAt time.Time `json:"generated_at,omitempty"`
	Models      []Model   `json:"models"`
}

var (
	loadOnce sync.Once
	loadErr  error
	loaded   File
	byProv   map[string][]Model
)

func load() {
	loadOnce.Do(func() {
		if len(catalogJSON) == 0 {
			byProv = map[string][]Model{}
			return
		}
		if err := json.Unmarshal(catalogJSON, &loaded); err != nil {
			loadErr = fmt.Errorf("catalog: parse catalog.gen.json: %w", err)
			byProv = map[string][]Model{}
			return
		}
		byProv = make(map[string][]Model, 4)
		for _, m := range loaded.Models {
			byProv[m.Provider] = append(byProv[m.Provider], m)
		}
		for k := range byProv {
			ms := byProv[k]
			sort.SliceStable(ms, func(i, j int) bool {
				if !ms[i].ReleasedAt.Equal(ms[j].ReleasedAt) {
					return ms[i].ReleasedAt.After(ms[j].ReleasedAt)
				}
				return ms[i].ID < ms[j].ID
			})
			byProv[k] = ms
		}
	})
}

// Get returns the catalog entries for one provider, sorted newest-
// first by ReleasedAt with ID as tiebreak. Returns an empty slice
// when the catalog is empty or the provider isn't known. Never
// returns nil. The returned slice is shared — callers must not
// mutate it.
//
// openai-auth is special-cased here (not just in List) because
// callers across the wizard / TUI / picker historically reach for
// Get directly. The result for openai-auth comes from the runtime
// per-user models file written by post-login scans, not from
// catalog.gen.json — see openAIAuthModels in list.go.
func Get(provider string) []Model {
	load()
	if provider == "openai-auth" {
		if ms := openAIAuthModels(); ms != nil {
			return ms
		}
		return []Model{}
	}
	if provider == "copilot" {
		if ms := copilotCachedModels(); ms != nil {
			return ms
		}
		return []Model{}
	}
	if ms, ok := byProv[provider]; ok {
		return ms
	}
	return []Model{}
}

// All returns every model across every provider. Useful for the
// debug `/doctor` view; not used by the picker (which is always
// scoped to one provider). The returned slice is shared — callers
// must not mutate it.
func All() []Model {
	load()
	return loaded.Models
}

// GeneratedAt returns the timestamp the embedded catalog was last
// refreshed. Zero when the catalog is empty or pre-dates the field.
func GeneratedAt() time.Time {
	load()
	return loaded.GeneratedAt
}

// LoadError returns any parse error that occurred when loading the
// embedded catalog. nil on success or when the catalog is empty by
// design.
func LoadError() error {
	load()
	return loadErr
}
