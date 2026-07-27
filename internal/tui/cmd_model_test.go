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

// The /model <name> shortcut can side-effect-switch providers
// (modelShortcutSwitch's doc comment). reasoningEffort is a
// session-global field the shortcut never clears or re-validates, so a
// level that applied cleanly on the old model must be re-flagged as a
// no-op the moment the new model can't use it — otherwise the setting
// silently stops applying with nothing telling the user.
func TestModelShortcut_WarnsWhenEffortBecomesNoop(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic-test"
kind = "anthropic"
base_url = "https://anthropic.example/v1"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai-test"
kind = "openai"
base_url = "https://openai.example/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/model claude-sonnet-4-6")
	m, _ = typeAndEnter(t, m, "/effort high")
	if strings.Contains(m.transcript.String(), "no-op on this model") {
		t.Fatalf("effort should apply cleanly on claude-sonnet-4-6; transcript:\n%s", m.transcript.String())
	}
	pre := m.transcript.String()
	m, _ = typeAndEnter(t, m, "/model gpt-4o")
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "no-op on this model") {
		t.Errorf("switching to gpt-4o (non-reasoning) should re-surface the no-op warning; new transcript:\n%s", post)
	}
}
