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
