package catalog

import (
	"context"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/config"
)

// curatedKinds is the set of provider kinds whose model catalog ships
// inside the binary (catalog.gen.json, refreshed by
// `go run ./cmd/yotta-models refresh`). Anything else falls through
// to a live HTTP fetch — necessary for Ollama (locally-installed
// models are runtime state) and for openai-compatible endpoints
// (xAI, NVIDIA NIM, custom proxies — too varied to script-curate).
//
// openai-auth is curated but separate from openai: its allow-list is
// per-user (depends on the user's ChatGPT plan) and discovered by a
// scan run after each successful login. The scan persists OK results
// to ~/.yottacode/auth/openai-auth-models.json; openAIAuthModels()
// below reads that file and wraps the IDs into catalog.Model.
var curatedKinds = map[string]bool{
	"anthropic":   true,
	"openai":      true,
	"openai-auth": true,
	"gemini":      true,
}

// openAIAuthLabels maps the known openai-auth model IDs to friendly
// display names for the picker. Unknown IDs (e.g. a candidate added
// ad-hoc via `yottacode openai-auth login --candidates`) fall through
// to using the ID as the display name in wrapOpenAIAuthIDs.
var openAIAuthLabels = map[string]string{
	"gpt-5.5":       "GPT-5.5",
	"gpt-5.4":       "GPT-5.4",
	"gpt-5.4-mini":  "GPT-5.4 Mini",
	"gpt-5.3-codex": "GPT-5.3 Codex",
	"gpt-5.2":       "GPT-5.2",
}

// openAIAuthModels reads ~/.yottacode/auth/openai-auth-models.json
// (written after every successful login) and converts the IDs to
// catalog.Model. Returns nil when the file is missing or unreadable —
// pickers and `/model list` should render an empty-state hint
// ("no models discovered yet — run `yottacode openai-auth login`").
//
// Never errors; the picker must always have something to show, even
// if that something is "log in first."
func openAIAuthModels() []Model {
	path, err := openaiauth.DefaultModelsPath()
	if err != nil {
		return nil
	}
	mf, err := openaiauth.LoadModels(path)
	if err != nil || len(mf.Models) == 0 {
		return nil
	}
	return wrapOpenAIAuthIDs(mf.Models)
}

// wrapOpenAIAuthIDs lifts bare IDs into catalog.Model, attaching
// friendly display names from openAIAuthLabels with the ID as a
// fallback for unrecognised entries.
func wrapOpenAIAuthIDs(ids []string) []Model {
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		display, ok := openAIAuthLabels[id]
		if !ok {
			display = id
		}
		out = append(out, Model{
			ID:          id,
			DisplayName: display,
			Provider:    "openai-auth",
		})
	}
	return out
}

// List returns the model list a picker should display for one
// provider profile. Curated providers are read out of the embedded
// catalog (or, for openai-auth, the runtime allow-list) and never
// touch the network; non-curated providers are fetched live each
// call. The signature is the same in both cases so callers don't
// branch.
//
// Errors are returned only for live fetches — curated reads are
// in-memory and infallible. An empty catalog (initial state, before
// the maintainer runs the refresh command) returns an empty slice
// with no error.
func List(ctx context.Context, p config.Provider, apiKey string) ([]Model, error) {
	if curatedKinds[p.Kind] {
		return Get(p.Kind), nil
	}
	return Live(ctx, p.Kind, p.BaseURL, apiKey)
}

// IsCurated reports whether p's kind is sourced from the embedded
// catalog. Useful when callers want to render different empty-state
// hints ("run `yotta-models refresh`" vs "couldn't reach API").
func IsCurated(p config.Provider) bool {
	return curatedKinds[p.Kind]
}

// IsCuratedKind is the kind-only flavor of IsCurated, for callers
// that have a kind string but no full config.Provider in scope (e.g.
// the wizard, where models live in CatalogEntry, not config.Provider).
func IsCuratedKind(kind string) bool {
	return curatedKinds[kind]
}
