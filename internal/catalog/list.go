package catalog

import (
	"context"
	"strings"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
	"github.com/yottadynamics/yottacode/internal/config"
)

// curatedKinds is the set of provider kinds whose model catalog ships
// inside the binary (catalog.gen.json, refreshed by
// `go run ./cmd/yotta-models refresh`). Anything else falls through
// to a live HTTP fetch — necessary for Ollama (locally-installed
// models are runtime state) and for openai-compatible endpoints
// (NVIDIA NIM, custom proxies — too varied to script-curate).
//
// openai-auth is curated but separate from openai: its allow-list is
// per-user (depends on the user's ChatGPT plan) and discovered by a
// scan run after each successful login. The scan persists OK results
// to ~/.yottacode/auth/openai-auth-models.json; openAIAuthModels()
// below reads that file and wraps the IDs into catalog.Model.
// The vertex kinds are curated from the models.dev snapshot alone (see
// modelsDevAugment). They must not fall through to Live: Vertex has no
// /models route on its chat shim, and its publisher-list endpoint returns
// the whole Model Garden — image classifiers, self-deploy-only entries,
// and models the caller's region won't serve.
var curatedKinds = map[string]bool{
	"anthropic":        true,
	"openai":           true,
	"openai-auth":      true,
	"copilot":          true,
	"gemini":           true,
	"xai":              true,
	"vertex":           true,
	"vertex-anthropic": true,
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
	return wrapOpenAIAuthIDs(mf.Models, mf.DisplayNames)
}

// wrapOpenAIAuthIDs lifts bare IDs into catalog.Model, attaching the
// display name the live catalog scan recorded for each slug (see
// openaiauth.ScanAndPersist). Falls back to the bare ID when the
// models file predates DisplayNames or the scan didn't get one for
// that slug.
func wrapOpenAIAuthIDs(ids []string, displayNames map[string]string) []Model {
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		display := displayNames[id]
		if display == "" {
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

// copilotCachedModels reads ~/.yottacode/auth/copilot-models.json
// and converts the entries to catalog.Model. Returns nil when the
// file is missing — pickers render an empty-state hint.
func copilotCachedModels() []Model {
	path, err := copilotauth.DefaultModelsPath()
	if err != nil {
		return nil
	}
	mf, err := copilotauth.LoadModels(path)
	if err != nil || len(mf.Models) == 0 {
		return nil
	}
	out := make([]Model, 0, len(mf.Models))
	for _, m := range mf.Models {
		out = append(out, Model{
			ID:            m.ID,
			DisplayName:   m.Name,
			Provider:      "copilot",
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
			Disabled:      m.Disabled,
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
// Models the user declared under [[providers.models]] are merged in
// either way, and stand in for the list when a live fetch fails: plenty
// of real OpenAI-compatible endpoints implement chat/completions and no
// /models route at all (Vertex's Gemini shim 404s on it), and a user who
// hand-listed their models has already told us what the endpoint serves.
// Leaving the picker empty in that case makes the config a lie.
//
// A live-fetch error is returned even when declared models cover for it,
// so callers can say so — the picker notes the failed fetch under the
// list, and `model fetch` prints a warning. Swallowing it would make an
// unreachable endpoint look healthy, which for Ollama usually means the
// daemon is down and the user needs to know. Callers that only want a
// list can ignore the error when the slice is non-empty.
//
// Errors never come from curated reads, which are in-memory and
// infallible. An empty catalog (initial state, before the maintainer
// runs the refresh command) returns an empty slice with no error.
func List(ctx context.Context, p config.Provider, apiKey string) ([]Model, error) {
	declared := configuredModels(p)
	if curatedKinds[p.Kind] {
		return annotateVertexAccess(p, MergeModels(Curated(p.Kind), declared)), nil
	}
	live, err := Live(ctx, p.Kind, p.BaseURL, apiKey)
	if err != nil {
		return declared, err
	}
	return MergeModels(live, declared), nil
}

func annotateVertexAccess(p config.Provider, models []Model) []Model {
	if p.Kind != vertexauth.ProviderVertex && p.Kind != vertexauth.ProviderVertexAnthropic {
		return models
	}
	access, ok := vertexauth.AccessMap(p.Kind, p.BaseURL)
	if !ok {
		return models
	}
	out := append([]Model(nil), models...)
	for i := range out {
		if vertexAccessDenied(p.Kind, out[i].ID, access) {
			out[i].Disabled = true
		}
	}
	return out
}

func vertexAccessDenied(kind, id string, access map[string]bool) bool {
	for _, candidate := range vertexAccessIDs(kind, id) {
		if allowed, seen := access[candidate]; seen && !allowed {
			return true
		}
	}
	return false
}

func vertexAccessIDs(kind, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	out := []string{id}
	add := func(v string) {
		if v == "" {
			return
		}
		for _, seen := range out {
			if seen == v {
				return
			}
		}
		out = append(out, v)
	}
	if kind == vertexauth.ProviderVertex {
		if strings.HasPrefix(id, "gemini") {
			add("google/" + id)
		}
		if strings.HasPrefix(id, "google/gemini") {
			add(strings.TrimPrefix(id, "google/"))
		}
	}
	return out
}

// configuredModels lifts a provider profile's [[providers.models]] into
// catalog entries. These carry only what the user wrote — a name, and
// optionally a context window — so the picker renders "—" for the rest,
// exactly as it does for live-fetched ids.
func configuredModels(p config.Provider) []Model {
	if len(p.Models) == 0 {
		return nil
	}
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		out = append(out, Model{
			ID:            m.Name,
			DisplayName:   m.Name,
			Provider:      p.Kind,
			ContextWindow: m.ContextWindow,
		})
	}
	return out
}

// DiscoverContextWindow resolves a model's context window from the
// provider's live API — no hardcoded per-model table. It reads the
// list-models endpoint first (which surfaces max_model_len /
// context_length / context_window on the backends that report them —
// vLLM, many NVIDIA NIM deployments, OpenRouter, Together) and falls
// back to the local models.dev copy for endpoints that list no window.
// Returns 0 when neither source yields a positive window, leaving the
// caller on default_window. Both sources report ADVERTISED limits;
// when a backend enforces something smaller, the TUI's passive drift
// correction (internal/tui/window_drift.go) observes it from live
// traffic and pins the measured value in the runtime overlay.
//
// The window is a per-DEPLOYMENT fact, not a per-model constant: a NIM
// can serve a model with a --max-model-len far below the model's
// architectural maximum to fit GPU memory, and new models ship daily, so
// it must be read from the live endpoint rather than guessed from the
// model name. Curated providers (Anthropic, OpenAI-auth, …) carry the
// window in the embedded catalog and never reach the probe.
//
// The second return is a short human-readable diagnostic (which source
// answered, or why none did) so callers can tell the user what happened
// instead of silently falling back to default_window.
func DiscoverContextWindow(ctx context.Context, p config.Provider, apiKey, model string) (int, string) {
	if model == "" {
		return 0, "no model"
	}
	// Ollama is special: /api/tags lists no window and Ollama doesn't
	// reject on context overflow, so the openai-compatible overflow probe
	// can't address it. /api/show, however, reports the model's context
	// length — query it directly for just this model (cheaper than listing
	// every model and enriching each).
	if p.Kind == "ollama" {
		if w := ollamaContextWindow(ctx, p.BaseURL, model); w > 0 {
			return w, "from ollama /api/show"
		}
		// Fall through to models.dev (covers ollama-cloud); local Ollama
		// won't host-match, so it just returns 0 below.
		if w := ModelsDevWindow(p.BaseURL, model); w > 0 {
			return w, "from models.dev"
		}
		return 0, "ollama: no context length in /api/show (pin `num_ctx` in the Modelfile or set context_window in config)"
	}
	if entries, err := List(ctx, p, apiKey); err == nil {
		for _, e := range entries {
			if e.ID == model && e.ContextWindow > 0 {
				return e.ContextWindow, "from list-models"
			}
		}
	}
	// models.dev catalog: the reliable, per-deployment source for hosted
	// OpenAI-compatible providers (NVIDIA NIM, Together, Fireworks…) whose
	// /v1/models lists only ids. Matched by base-URL host so we get THIS
	// deployment's window (NVIDIA serves deepseek-v4-pro at 1M; other hosts
	// at 64K). This replaces the old overflow probe, which was slow,
	// regex-brittle, and stalled on large-window models (the server
	// prefills the oversized prompt instead of rejecting).
	if w := ModelsDevWindow(p.BaseURL, model); w > 0 {
		return w, "from models.dev"
	}
	return 0, "no window from list-models or models.dev (set a per-model context_window in config to override)"
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
