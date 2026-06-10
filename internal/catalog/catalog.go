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

// Curated returns the offline model catalog for a curated provider kind.
// Gemini is augmented from the local models.dev snapshot so the picker can
// offer newly published Gemini IDs even when catalog.gen.json lags.
func Curated(provider string) []Model {
	models := Get(provider)
	if provider == "gemini" {
		models = MergeModels(models, ModelsDevModelsByProvider("google", "gemini"))
	}
	return models
}

// All returns every model across every provider. Useful for the
// debug `/doctor` view; not used by the picker (which is always
// scoped to one provider). The returned slice is shared — callers
// must not mutate it.
func All() []Model {
	load()
	return loaded.Models
}

// MergeModels appends models from extra that are not already present in base.
// The first occurrence of an ID wins, preserving the embedded catalog's
// display names and ordering while allowing runtime/local catalogs to backfill
// newer provider models for picker use.
func MergeModels(base, extra []Model) []Model {
	out := append([]Model(nil), base...)
	seen := make(map[string]struct{}, len(out))
	for _, m := range out {
		seen[m.ID] = struct{}{}
	}
	for _, m := range extra {
		if _, ok := seen[m.ID]; ok {
			continue
		}
		seen[m.ID] = struct{}{}
		out = append(out, m)
	}
	return out
}

// FindByID returns the embedded-catalog entry whose ID matches,
// searching across every provider. Model IDs are effectively globally
// unique (claude-*, gemini-*, gpt-*, …), so the first match is the
// right one. ok is false when nothing matches — including for the
// runtime-sourced openai-auth/copilot sets, which carry no token
// limits or capability flags worth reasoning over. Callers then leave
// catalog-derived fields zero/nil.
func FindByID(id string) (Model, bool) {
	load()
	for _, m := range loaded.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// ReasoningInfo returns the two catalog facts the adapter needs to size
// an extended-thinking budget for budget-based providers (Anthropic,
// Gemini): the model's max-output tokens and its thinking-capability
// tristate. Both are zero/nil when the model isn't in the catalog — the
// adapter then leaves reasoning at the provider default. Cheap enough
// to call on every adapter (re)build.
func ReasoningInfo(modelID string) (maxOutput int, supportsThinking *bool) {
	if m, ok := FindByID(modelID); ok {
		return m.MaxOutput, m.Capabilities.Thinking
	}
	return 0, nil
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
