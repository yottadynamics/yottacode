package main

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// The Vertex providers are intentionally not fetched through a Vertex API
// during yotta-models refresh: Gemini's OpenAI-compatible shim has no
// useful /models route, and the publisher endpoint returns all of Model
// Garden rather than the chat models this adapter can drive. Refresh must
// still download the public models.dev snapshot that contains the Vertex
// rows Curated("vertex*") reads at runtime.
func TestRefreshCatalogIncludesVertexModelsDevProviders(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
	}{
		{provider: "google-vertex", model: "gemini-2.5-pro"},
		{provider: "google-vertex-anthropic", model: "claude-sonnet-4-5@20250929"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			ctx, out := catalog.ModelsDevLimitsByProvider(tc.provider, tc.model)
			if ctx == 0 || out == 0 {
				t.Fatalf("models.dev snapshot missing %s/%s limits: context=%d output=%d", tc.provider, tc.model, ctx, out)
			}
		})
	}
}

func TestModelsDevBackfillMapDocumentsFetchedProvidersOnly(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "gemini", "xai"} {
		if modelsDevProviderID[provider] == "" {
			t.Fatalf("modelsDevProviderID[%q] missing", provider)
		}
	}
	for _, provider := range []string{"vertex", "vertex-anthropic"} {
		if _, ok := modelsDevProviderID[provider]; ok {
			t.Fatalf("%s should not be in modelsDevProviderID: Vertex lists come directly from models-dev.gen.json via catalog.Curated, not from catalog.gen.json backfill", provider)
		}
	}
}
