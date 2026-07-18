package catalog

import (
	"strings"
	"testing"
)

// The vertex kinds are curated entirely from the models.dev snapshot —
// there are no vertex rows in catalog.gen.json — so an empty list here
// means the augment table is wired wrong, not that the catalog is stale.
func TestCurated_VertexKindsComeFromModelsDev(t *testing.T) {
	for _, kind := range []string{"vertex", "vertex-anthropic"} {
		if got := Curated(kind); len(got) == 0 {
			t.Errorf("Curated(%q) is empty; expected models backfilled from the models.dev snapshot", kind)
		}
	}
}

// models.dev's google-vertex is a superset: Gemini, Claude, and
// third-party *-maas entries that Vertex only serves once you deploy them
// yourself. The vertex kind drives an OpenAI-compatible Gemini shim that
// can serve none of the others, so the prefix filter is what keeps
// uncallable models out of the picker.
func TestCurated_VertexIsGeminiOnly(t *testing.T) {
	models := Curated("vertex")
	for _, m := range models {
		if !strings.HasPrefix(m.ID, "google/gemini") {
			t.Errorf("Curated(\"vertex\") offers %q; the Gemini shim needs publisher-qualified Gemini ids", m.ID)
		}
	}
	if !hasModelID(models, "google/gemini-2.5-pro") {
		t.Error("google/gemini-2.5-pro missing from the vertex list")
	}
}

// Claude on Vertex is only addressable by version-qualified id — the bare
// id 404s — so the list must carry the @suffix through.
func TestCurated_VertexAnthropicIsClaudeOnlyAndVersioned(t *testing.T) {
	models := Curated("vertex-anthropic")
	for _, m := range models {
		if !strings.HasPrefix(m.ID, "claude") {
			t.Errorf("Curated(\"vertex-anthropic\") offers %q; this kind only speaks to Anthropic publisher models", m.ID)
		}
		if !strings.Contains(m.ID, "@") {
			t.Errorf("model %q carries no @version; Vertex will not serve a bare id", m.ID)
		}
	}
	if !hasModelID(models, "claude-sonnet-4-5@20250929") {
		t.Error("claude-sonnet-4-5@20250929 missing from the vertex-anthropic list")
	}
}

// Curated stamps merged entries with our kind rather than the models.dev
// provider id they arrive with ("google-vertex").
func TestCurated_NormalizesProviderToKind(t *testing.T) {
	for _, kind := range []string{"vertex", "vertex-anthropic", "gemini"} {
		for _, m := range Curated(kind) {
			if m.Provider != kind {
				t.Errorf("Curated(%q) returned %q with Provider=%q; want the kind", kind, m.ID, m.Provider)
			}
		}
	}
}

// Get returns a slice shared with the embedded catalog. Curated normalizes
// Provider in place, so it must only ever write to a copy.
func TestCurated_DoesNotMutateEmbeddedCatalog(t *testing.T) {
	_ = Curated("gemini")
	_ = Curated("vertex")
	_ = Curated("vertex-anthropic")
	for _, provider := range []string{"anthropic", "openai", "gemini"} {
		for _, m := range Get(provider) {
			if m.Provider != provider {
				t.Fatalf("embedded catalog corrupted: %s now claims provider %q, want %q", m.ID, m.Provider, provider)
			}
		}
	}
}

// Vertex qualifies Claude ids with a version suffix the vendor catalog
// spells differently. Without the variants the model reads as
// uncatalogued and silently loses its extended-thinking budget.
func TestReasoningInfo_ResolvesVertexVersionedIDs(t *testing.T) {
	// Anchor on whatever the embedded catalog actually holds, so this
	// tests the id transform rather than catalog freshness.
	var bare, dated string
	for _, m := range Get("anthropic") {
		if m.MaxOutput == 0 {
			continue
		}
		// Anthropic lists pinned snapshots as <model>-<8-digit date>.
		if i := strings.LastIndex(m.ID, "-"); i > 0 && len(m.ID)-i-1 == 8 && isAllDigits(m.ID[i+1:]) {
			if dated == "" {
				dated = m.ID
			}
			continue
		}
		if bare == "" {
			bare = m.ID
		}
	}

	t.Run("@default resolves to the bare id", func(t *testing.T) {
		if bare == "" {
			t.Skip("embedded catalog has no unversioned anthropic model; run cmd/yotta-models refresh")
		}
		wantMax, _ := ReasoningInfo(bare)
		gotMax, gotThinking := ReasoningInfo(bare + "@default")
		if gotMax != wantMax {
			t.Errorf("ReasoningInfo(%q@default) maxOutput = %d, want %d", bare, gotMax, wantMax)
		}
		if gotThinking == nil {
			t.Error("thinking = nil; @default lost the capability flag")
		}
	})

	t.Run("@date maps onto the vendor's -date id", func(t *testing.T) {
		if dated == "" {
			t.Skip("embedded catalog has no date-pinned anthropic model; run cmd/yotta-models refresh")
		}
		i := strings.LastIndex(dated, "-")
		vertexID := dated[:i] + "@" + dated[i+1:] // claude-x-20250929 -> claude-x@20250929
		wantMax, _ := ReasoningInfo(dated)

		gotMax, gotThinking := ReasoningInfo(vertexID)
		if gotMax != wantMax {
			t.Errorf("ReasoningInfo(%q) maxOutput = %d, want %d — same model as %q, only the separator differs",
				vertexID, gotMax, wantMax, dated)
		}
		if gotThinking == nil {
			t.Errorf("ReasoningInfo(%q) thinking = nil; the pinned snapshot lost its capability flag", vertexID)
		}
	})
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Vertex's Gemini shim namespaces ids by publisher
// (google/gemini-2.5-pro), and resellers like OpenRouter do the same.
// The catalog only ever carries the bare id, so without stripping the
// prefix the model reads as uncatalogued and loses its thinking budget.
func TestReasoningInfo_StripsPublisherPrefix(t *testing.T) {
	wantMax, wantThinking := ReasoningInfo("gemini-2.5-pro")
	if wantMax == 0 {
		t.Skip("embedded catalog has no gemini-2.5-pro; run cmd/yotta-models refresh")
	}
	gotMax, gotThinking := ReasoningInfo("google/gemini-2.5-pro")
	if gotMax != wantMax {
		t.Errorf("maxOutput = %d, want %d — same model, publisher-qualified id", gotMax, wantMax)
	}
	if gotThinking == nil {
		t.Fatal("thinking = nil; the publisher-qualified id lost its capability flag")
	}
	if *gotThinking != *wantThinking {
		t.Errorf("thinking = %v, want %v", *gotThinking, *wantThinking)
	}
}

func TestLookupIDVariants(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "gemini-2.5-pro", want: []string{"gemini-2.5-pro"}},
		{in: "google/gemini-2.5-pro", want: []string{"google/gemini-2.5-pro", "gemini-2.5-pro"}},
		// @default means "latest", which is the bare id. There is no
		// claude-opus-4-8-default to try.
		{in: "claude-opus-4-8@default", want: []string{"claude-opus-4-8@default", "claude-opus-4-8"}},
		// A pinned snapshot: Vertex writes model@date, the vendor catalog
		// writes model-date. The dated form is the exact match and must be
		// tried before the bare one.
		{
			in:   "claude-sonnet-4-5@20250929",
			want: []string{"claude-sonnet-4-5@20250929", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		},
		{
			in: "anthropic/claude-sonnet-4-5@20250929",
			want: []string{
				"anthropic/claude-sonnet-4-5@20250929",
				"anthropic/claude-sonnet-4-5-20250929",
				"claude-sonnet-4-5-20250929",
				"anthropic/claude-sonnet-4-5",
				"claude-sonnet-4-5",
			},
		},
		{in: "", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := lookupIDVariants(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("lookupIDVariants(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("variant %d = %q, want %q (most specific first)", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The family prefix in modelsDevAugment selects "gemini", which sweeps in
// siblings that share the name but cannot chat.
func TestCurated_DropsNonChatModels(t *testing.T) {
	for _, kind := range []string{"vertex", "gemini"} {
		for _, m := range Curated(kind) {
			if strings.Contains(strings.ToLower(m.ID), "embedding") {
				t.Errorf("Curated(%q) offers embedding model %q as a chat model", kind, m.ID)
			}
			if strings.HasSuffix(strings.ToLower(m.ID), "-tts") {
				t.Errorf("Curated(%q) offers text-to-speech model %q as a chat model", kind, m.ID)
			}
		}
	}
	// Image models take a chat-shaped request and are deliberately kept.
	if !hasModelID(Curated("vertex"), "google/gemini-2.5-pro") {
		t.Error("the filter removed google/gemini-2.5-pro")
	}
}

func TestCurated_GeminiUsesModelsDevLimitsForDuplicateIDs(t *testing.T) {
	ctx, out := ModelsDevLimitsByProvider("google", "gemini-2.5-pro")
	if ctx == 0 || out == 0 {
		t.Skip("models.dev snapshot has no gemini-2.5-pro limits")
	}
	for _, m := range Curated("gemini") {
		if m.ID != "gemini-2.5-pro" {
			continue
		}
		if m.ContextWindow != ctx || m.MaxOutput != out {
			t.Fatalf("gemini-2.5-pro limits = (%d,%d), want models.dev (%d,%d)", m.ContextWindow, m.MaxOutput, ctx, out)
		}
		return
	}
	t.Fatal("gemini-2.5-pro missing from curated Gemini list")
}

func TestIsNonChatModel(t *testing.T) {
	tests := []struct {
		model Model
		want  bool
	}{
		{model: Model{ID: "gemini-2.5-pro", MaxOutput: 65536}, want: false},
		{model: Model{ID: "gemini-2.5-flash-image", MaxOutput: 32768}, want: false},
		{model: Model{ID: "claude-opus-4-8@default", MaxOutput: 128000}, want: false},
		{model: Model{ID: "gemini-embedding-001", MaxOutput: 1}, want: true},
		{model: Model{ID: "gemini-2.5-flash-tts", MaxOutput: 16384}, want: true},
		{model: Model{ID: "gemini-2.5-flash-preview-tts", MaxOutput: 16384}, want: true},
		// Output limit of 1 token is the tell when the name isn't.
		{model: Model{ID: "some-vector-model", MaxOutput: 1}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.model.ID, func(t *testing.T) {
			if got := isNonChatModel(tc.model); got != tc.want {
				t.Errorf("isNonChatModel(%q) = %v, want %v", tc.model.ID, got, tc.want)
			}
		})
	}
}

func TestReasoningInfo_UnknownModelStaysUnknown(t *testing.T) {
	maxOut, thinking := ReasoningInfo("not-a-real-model@default")
	if maxOut != 0 || thinking != nil {
		t.Errorf("ReasoningInfo(unknown) = (%d, %v), want (0, nil) so the adapter keeps the provider default", maxOut, thinking)
	}
}

func TestIsCuratedKind_VertexNeverFallsThroughToLive(t *testing.T) {
	for _, kind := range []string{"vertex", "vertex-anthropic"} {
		if !IsCuratedKind(kind) {
			t.Errorf("IsCuratedKind(%q) = false; it would hit Live, and Vertex has no /models route", kind)
		}
	}
}

func hasModelID(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
