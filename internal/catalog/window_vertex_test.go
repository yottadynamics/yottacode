package catalog

import "testing"

// sentinelWindow is deliberately not any real model's window, so a test
// that sees it knows resolution fell all the way through rather than
// coincidentally matching.
const sentinelWindow = 4242

// A vertex profile serving google/gemini-2.5-pro used to find nothing at
// any layer and land on context.default_window — reporting 128k for a
// 1M-context model, which fires auto-summarize at an eighth of the real
// budget. Every lookup missed for a different reason: the generated
// catalog has no `vertex` provider, the publisher-qualified id matches no
// catalog row verbatim, and the model-tag store is keyed on `gemini-*`
// rather than `google/gemini-*`.
func TestResolveWindowForProvider_VertexResolvesHostQualifiedIDs(t *testing.T) {
	tests := []struct {
		name        string
		kind, model string
		want        int
	}{
		{
			name:  "publisher-prefixed gemini on the shim",
			kind:  "vertex",
			model: "google/gemini-2.5-pro",
			want:  1048576,
		},
		{
			name:  "date-pinned claude on vertex",
			kind:  "vertex-anthropic",
			model: "claude-sonnet-4-5@20250929",
			want:  200000,
		},
		{
			name:  "@default claude on vertex",
			kind:  "vertex-anthropic",
			model: "claude-opus-4-8@default",
			want:  1000000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveWindowForProvider(tc.kind, tc.model, 0, sentinelWindow)
			if got == sentinelWindow {
				t.Fatalf("%s/%s fell through to the default window; the session would size its context budget from context.default_window",
					tc.kind, tc.model)
			}
			if got != tc.want {
				t.Errorf("window = %d, want %d", got, tc.want)
			}
		})
	}
}

// An explicit per-model override is the user's own measurement and must
// still beat the new curated layer. (window_test.go covers the override
// generally; this pins it for the host-qualified ids, where the curated
// lookup is the one that would otherwise answer.)
func TestResolveWindowForProvider_OverrideBeatsCuratedLookup(t *testing.T) {
	got := ResolveWindowForProvider("vertex", "google/gemini-2.5-pro", 4096, sentinelWindow)
	if got != 4096 {
		t.Errorf("window = %d, want the explicit override 4096", got)
	}
}

// The whole reason windows are resolved per provider: the same id has
// different real limits per backend, and a curated lookup must not leak
// one host's number to another's namesake.
func TestCuratedWindowForProvider_DoesNotLeakAcrossKinds(t *testing.T) {
	if _, ok := curatedWindowForProvider("openai-compatible", "gemini-2.5-pro"); ok {
		t.Error("a generic openai-compatible proxy resolved a window from Gemini's curated list")
	}
	if _, ok := curatedWindowForProvider("", "gemini-2.5-pro"); ok {
		t.Error("an empty provider kind resolved a curated window")
	}
	if _, ok := curatedWindowForProvider("vertex", "not-a-real-model"); ok {
		t.Error("an unknown model resolved a curated window")
	}
}

// Vertex ids are host-qualified in Curated(), but callers may still
// pass a bare Gemini id from older configs; both should resolve.
func TestCuratedWindowForProvider_MatchesMostSpecificFirst(t *testing.T) {
	got, ok := curatedWindowForProvider("vertex", "gemini-2.5-pro")
	if !ok {
		t.Fatal("bare gemini-2.5-pro did not resolve under the vertex kind")
	}
	pref, ok := curatedWindowForProvider("vertex", "google/gemini-2.5-pro")
	if !ok {
		t.Fatal("publisher-qualified id did not resolve under the vertex kind")
	}
	if got != pref {
		t.Errorf("qualified id resolved %d but bare id resolved %d; same model, same host", pref, got)
	}
}
