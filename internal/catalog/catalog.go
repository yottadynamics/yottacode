package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

// modelsDevAugment maps a curated provider kind onto the models.dev
// provider whose list backfills it, plus the model-id prefix worth
// keeping. The snapshot is the only source for the vertex kinds — there
// is no fetchVertex in cmd/yotta-models, because Vertex publishes the
// same models Google and Anthropic already do.
//
// The prefix is load-bearing, not cosmetic. models.dev's google-vertex is
// a superset: Gemini plus Claude plus third-party *-maas entries that
// Vertex only serves after you deploy them yourself. Listing those would
// offer models that cannot be called.
var modelsDevAugment = map[string]struct{ providerID, prefix string }{
	"gemini":           {providerID: "google", prefix: "gemini"},
	"vertex":           {providerID: "google-vertex", prefix: "gemini"},
	"vertex-anthropic": {providerID: "google-vertex-anthropic", prefix: "claude"},
}

// Curated returns the offline model catalog for a curated provider kind.
// Kinds in modelsDevAugment are backfilled from the local models.dev
// snapshot so the picker can offer newly published IDs even when
// catalog.gen.json lags. This is the entry point every picker surface
// (wizard, /provider add, /model) and model-ownership lookup should use
// for curated kinds; reach for Get only when the raw embedded catalog is
// specifically wanted.
func Curated(provider string) []Model {
	models := Get(provider)
	src, ok := modelsDevAugment[provider]
	if !ok {
		return models
	}
	dev := chatModelsOnly(ModelsDevModelsByProvider(src.providerID, src.prefix))
	// Gemini's generated catalog can lag models.dev limits even when it
	// already knows the ID. Merge with models.dev first for this provider
	// so current context/max-output values win, while still appending any
	// embedded-only display names below.
	if provider == "gemini" {
		models = MergeModels(dev, models)
	} else {
		// MergeModels copies, so the writes below can't reach the shared
		// slice Get handed back.
		models = MergeModels(models, dev)
	}
	if provider == "vertex" {
		models = qualifyVertexGeminiModels(models)
	}
	// ModelsDevModelsByProvider stamps entries with the models.dev
	// provider id ("google-vertex"), not our kind. Nothing reads .Provider
	// off a Curated result today, but leaving two namespaces mixed in one
	// slice is a trap for whoever does first.
	for i := range models {
		models[i].Provider = provider
	}
	return models
}

func qualifyVertexGeminiModels(models []Model) []Model {
	out := append([]Model(nil), models...)
	for i := range out {
		if strings.HasPrefix(out[i].ID, "gemini") {
			out[i].ID = "google/" + out[i].ID
		}
	}
	return out
}

// chatModelsOnly drops models.dev entries that share a family prefix
// with the chat models but cannot hold a conversation, so the picker
// stops offering them as if they could.
//
// The prefix filter in modelsDevAugment selects a family ("gemini",
// "claude"); it can't tell a chat model from its speech or embedding
// siblings, and models.dev carries no modality field to ask. (The
// generated catalog has no such problem: cmd/yotta-models filters on
// Gemini's supportedGenerationMethods at fetch time.) So this matches on
// the only signals available.
//
// Deliberately narrow. Image models stay: they take a chat-shaped
// request and return a message, so choosing one is a real if unusual
// choice, not a broken one.
func chatModelsOnly(models []Model) []Model {
	out := models[:0:0]
	for _, m := range models {
		if isNonChatModel(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func isNonChatModel(m Model) bool {
	id := strings.ToLower(m.ID)
	switch {
	// Embedding models return vectors. models.dev reports their output
	// limit as 1 token, which is the giveaway when the name isn't.
	case strings.Contains(id, "embedding"), m.MaxOutput == 1:
		return true
	// Text-to-speech returns audio.
	case strings.HasSuffix(id, "-tts"), strings.Contains(id, "-tts-"):
		return true
	}
	return false
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
// The first occurrence of an ID wins, preserving the caller's preferred
// ordering and metadata while allowing runtime/local catalogs to backfill
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

// FindByProviderID returns the catalog entry for id owned by the given
// provider. Unlike FindByID it never crosses provider namespaces: the
// same model id served through a different backend (gpt-5.5 via the
// ChatGPT Codex backend vs api.openai.com) is a different deployment
// with different limits, so a namesake's facts must not leak.
//
// For the runtime-sourced kinds the per-user scan set stands in for
// the embedded catalog — copilot's scan captures real per-backend
// token limits that exist nowhere else (openai-auth's scan carries
// bare ids, so its entries simply never satisfy window>0 checks).
func FindByProviderID(provider, id string) (Model, bool) {
	load()
	for _, m := range loaded.Models {
		if m.ID == id && strings.EqualFold(m.Provider, provider) {
			return m, true
		}
	}
	switch p := strings.ToLower(provider); p {
	case "copilot", "openai-auth":
		for _, m := range Get(p) {
			if m.ID == id {
				return m, true
			}
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
	for _, id := range lookupIDVariants(modelID) {
		if m, ok := FindByID(id); ok {
			return m.MaxOutput, m.Capabilities.Thinking
		}
	}
	return 0, nil
}

// lookupIDVariants returns modelID followed by the progressively
// unqualified forms worth trying against the catalog, most specific
// first.
//
// Hosts that resell someone else's model qualify the id, and the
// embedded catalog only ever carries the bare one. Two qualifiers show
// up in practice, and both can appear together:
//
//   - a publisher prefix — Vertex's google/gemini-2.5-pro, OpenRouter's
//     anthropic/claude-sonnet-4-5
//   - an @version suffix — Vertex's claude-sonnet-4-5@20250929, or
//     @default for "track the latest snapshot"
//
// Neither changes which model you are talking to, so the catalog's
// facts about the bare id apply. Without this the model reads as
// uncatalogued and silently loses its thinking budget — a capability
// downgrade with no error to notice.
//
// Stripping is safe here because FindByID is already deliberately
// global (see its doc): model ids are effectively unique across
// vendors, so an unqualified id resolves to the same model whoever is
// hosting it. Callers that must not cross a namespace use
// FindByProviderID instead.
func lookupIDVariants(modelID string) []string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	out := []string{modelID}
	add := func(id string) {
		if id == "" {
			return
		}
		for _, seen := range out {
			if seen == id {
				return
			}
		}
		out = append(out, id)
	}

	unversioned := modelID
	if base, version, ok := strings.Cut(modelID, "@"); ok {
		unversioned = base
		// Vertex spells a pinned snapshot model@20250929 where the
		// vendor's own catalog spells it model-20250929 — same model,
		// same date, different separator. Try that before dropping the
		// version, since the dated entry is the exact match and the bare
		// one may not exist at all (Anthropic lists
		// claude-sonnet-4-5-20250929 but no bare claude-sonnet-4-5).
		if version != "" && version != "default" {
			add(base + "-" + version)
			if i := strings.LastIndex(base, "/"); i >= 0 {
				add(base[i+1:] + "-" + version)
			}
		}
		// @default means "track the latest", which is what the bare id is.
		add(base)
	}
	// Only the last path segment is the model; a publisher prefix may
	// itself contain slashes on some hosts.
	if i := strings.LastIndex(unversioned, "/"); i >= 0 {
		add(unversioned[i+1:])
	}
	return out
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
