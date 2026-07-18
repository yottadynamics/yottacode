package catalog

import "strings"

// ResolveWindow is the single static (no-network) resolver every window
// consumer should call to size a model's context window. It layers the
// sources by authority, highest first:
//
//  1. override (>0) — the user's per-model context_window from config,
//     which is also where a successful live probe persists its result
//     (so this doubles as the observed, per-DEPLOYMENT cache).
//  2. the embedded catalog's ContextWindow for this exact model id —
//     the canonical per-MODEL value for curated providers (Anthropic,
//     Gemini, OpenAI). Consulting it here makes the catalog we already
//     maintain the authority for curated windows, instead of the coarse
//     model-tag prefix table.
//  3. WindowFor's prefix table — a conservative compiled fallback for
//     models neither overridden nor catalogued.
//  4. defaultWindow — the final floor.
//
// It deliberately does NOT touch the network; the live, per-deployment
// discovery (DiscoverContextWindow) runs separately and persists into the
// override layer above. Keeping the catalog (per-model, build-time) and the
// probe cache (per-deployment, runtime) as distinct layers behind one
// resolver is intentional: they are different kinds of fact with different
// keys and mutability (see DiscoverContextWindow).
func ResolveWindow(model string, override, defaultWindow int) int {
	if override <= 0 {
		if m, ok := FindByID(model); ok && m.ContextWindow > 0 {
			return m.ContextWindow
		}
	}
	return EffectiveWindow(model, override, defaultWindow)
}

// ResolveWindowForProvider is ResolveWindow for callers that know which
// provider kind serves the model. The same model id can have different
// real limits per backend — gpt-5.5 is 1.05M-context on api.openai.com
// but ~272k-input through the ChatGPT Codex backend (measured
// 2026-06-10: 264,995 input tokens accepted, ~281k rejected) — so two
// provider-scoped layers run ahead of ResolveWindow's per-model-id
// layers, after the override:
//
//  1. the window store under "<kind>/<model id>" — provider-qualified
//     entries (embedded baseline or the ~/.yottacode overlay, where
//     users can pin their own) carrying per-backend measurements the
//     per-model catalog cannot express;
//  2. the catalog entry for this exact (provider, id) pair — a curated
//     provider's number applies on its own backend only and never
//     leaks to a namesake model behind a different kind;
//  3. the provider's curated list, matched against the host-qualified
//     forms of the id (see curatedWindowForProvider).
//
// When no provider-scoped layer answers — openai-compatible proxies, an
// empty kind, models with no qualified facts — resolution degrades to
// ResolveWindow unchanged.
func ResolveWindowForProvider(provider, model string, override, defaultWindow int) int {
	if override <= 0 {
		kind := strings.ToLower(strings.TrimSpace(provider))
		tag := strings.ToLower(strings.TrimSpace(model))
		if kind != "" && tag != "" {
			if w, ok := windowStoreLookup(kind + "/" + tag); ok {
				return w
			}
			if m, ok := FindByProviderID(provider, model); ok && m.ContextWindow > 0 {
				return m.ContextWindow
			}
			if w, ok := curatedWindowForProvider(provider, model); ok {
				return w
			}
		}
	}
	return ResolveWindow(model, override, defaultWindow)
}

// curatedWindowForProvider answers from the provider's own curated list —
// the layer above (FindByProviderID) reads only the generated catalog,
// which carries no rows for kinds sourced from the models.dev snapshot,
// and matches the id verbatim.
//
// Both gaps bite the same model. A vertex profile serving
// google/gemini-2.5-pro found nothing at any layer: the generated catalog
// has no `vertex` provider, the publisher-qualified id matches no catalog
// row, and the model-tag store is keyed on `gemini-*`, not
// `google/gemini-*`. Resolution fell all the way through to
// context.default_window — reporting 128k for a 1M-context model, which
// silently fires auto-summarize at an eighth of the real budget.
//
// Curated() is the right source because it is already host-scoped: the
// same id carries different limits per backend (gemini-2.5-pro is 1M on
// Vertex and 128k through Copilot), which is exactly the distinction the
// models.dev snapshot preserves and a per-model-id lookup destroys.
func curatedWindowForProvider(provider, model string) (int, bool) {
	models := Curated(provider)
	if len(models) == 0 {
		return 0, false
	}
	// Most specific id form first, so a pinned snapshot never resolves to
	// a sibling's window.
	ids := lookupIDVariants(model)
	if provider == "vertex" && strings.HasPrefix(strings.TrimSpace(model), "gemini") {
		ids = append([]string{"google/" + strings.TrimSpace(model)}, ids...)
	}
	for _, id := range ids {
		for _, m := range models {
			if m.ID == id && m.ContextWindow > 0 {
				return m.ContextWindow, true
			}
		}
	}
	return 0, false
}

// EffectiveWindow resolves the context window from an explicit override
// and the built-in model-tag table only (no catalog lookup) — the lower
// two layers of ResolveWindow. An explicit per-model override (override >
// 0 — sourced from the user's config, typically captured from the
// provider's list-models endpoint or a live probe at registration time)
// wins over the model-tag table and the configured default. A non-positive
// override means "not set"; resolution then falls through to WindowFor.
//
// Prefer ResolveWindow, which also consults the catalog; EffectiveWindow is
// kept as the override+prefix primitive it builds on (and for callers that
// have no catalog model id to look up).
func EffectiveWindow(model string, override, defaultWindow int) int {
	if override > 0 {
		return override
	}
	return WindowFor(model, defaultWindow)
}

// WindowFor returns the context-window capacity (in tokens) for the
// given model tag from the file-backed window store, falling back to
// defaultWindow when no entry matches. Matching is by lowercase prefix —
// generous on purpose so unknown variants of known families still
// resolve, with the longest (most specific) prefix winning.
//
// The store is the LAST resort, consulted only when neither the user's
// override nor the curated catalog answered, so its values lean toward
// safe under-estimates (an over-estimate here would let a session overrun
// a smaller real window and hard-fail). Its data lives OUTSIDE the Go
// source — an embedded, committed baseline plus a runtime overlay
// (~/.yottacode/context-windows.json) — see windowstore.go. Users with an
// exotic model can pin a per-model context_window or context.default_window.
func WindowFor(model string, defaultWindow int) int {
	tag := strings.ToLower(strings.TrimSpace(model))
	if tag == "" {
		return defaultWindow
	}
	if w, ok := windowStoreLookup(tag); ok {
		return w
	}
	return defaultWindow
}
