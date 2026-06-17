package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

// modelsDevOnlyGeminiID returns a Gemini model ID present in the
// curated (models.dev-merged) catalog but absent from the embedded
// catalog.gen.json — the kind of entry only the merge can surface.
// Fails the test when the local models.dev snapshot adds nothing,
// since that would leave the merge paths untested.
func modelsDevOnlyGeminiID(t *testing.T) string {
	t.Helper()
	embedded := map[string]bool{}
	for _, m := range catalog.Get("gemini") {
		embedded[m.ID] = true
	}
	for _, m := range catalog.Curated("gemini") {
		if !embedded[m.ID] {
			return m.ID
		}
	}
	t.Fatalf("models.dev snapshot should add gemini models beyond the embedded catalog")
	return ""
}

// TestProviderOwnsModel_GeminiIncludesModelsDevMerge guards the
// /model <name> shortcut: a Gemini model that exists only in the
// models.dev augmentation must still resolve to the gemini profile,
// matching what the /model picker displays. Without this, picking a
// merged model in the picker and later typing /model <that-model>
// would silently lose the profile switch.
func TestProviderOwnsModel_GeminiIncludesModelsDevMerge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	merged := modelsDevOnlyGeminiID(t)
	p := &config.Provider{Name: "gemini", Kind: "gemini"}
	if !providerOwnsModel(p, merged) {
		t.Errorf("providerOwnsModel(%q) = false, want true for models.dev-merged model", merged)
	}
}

// TestFormatProviderModels_GeminiListsMergedCatalog verifies the
// /model list text dump shows the same merged Gemini catalog the
// picker overlay does — both surfaces read catalog.Curated.
func TestFormatProviderModels_GeminiListsMergedCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	merged := modelsDevOnlyGeminiID(t)
	p := &config.Provider{Name: "gemini", Kind: "gemini"}
	out := formatProviderModels(p, "")
	if !strings.Contains(out, merged) {
		t.Errorf("/model list should include models.dev-merged %s; got:\n%s", merged, out)
	}
}
