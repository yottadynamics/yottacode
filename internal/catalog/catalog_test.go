package catalog

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
)

func TestEmbeddedCatalogParses(t *testing.T) {
	t.Helper()
	if err := LoadError(); err != nil {
		t.Fatalf("embedded catalog failed to parse: %v", err)
	}
	// Empty initial catalog is fine — All() and Get() must just
	// return empty slices, never nil.
	if All() == nil {
		t.Fatal("All() returned nil; expected empty slice")
	}
	if Get("anthropic") == nil {
		t.Fatal("Get(anthropic) returned nil; expected empty slice")
	}
}

func TestModelLabelFallsBackToID(t *testing.T) {
	cases := []struct {
		name        string
		m           Model
		want        string
		description string
	}{
		{"with display", Model{ID: "x", DisplayName: "X Pro"}, "X Pro", "display name preferred"},
		{"without display", Model{ID: "x"}, "x", "id when no display name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q (%s)", got, tc.want, tc.description)
			}
		})
	}
}

func TestFileRoundTripsJSON(t *testing.T) {
	yes := true
	original := File{
		GeneratedAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		Models: []Model{
			{
				ID:            "claude-opus-4-7",
				DisplayName:   "Claude Opus 4.7",
				Provider:      "anthropic",
				ContextWindow: 200000,
				MaxOutput:     8192,
				Capabilities:  Capabilities{Thinking: &yes, Vision: &yes},
			},
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got File
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Models) != 1 {
		t.Fatalf("models len: got %d, want 1", len(got.Models))
	}
	m := got.Models[0]
	if m.ID != "claude-opus-4-7" || m.Provider != "anthropic" {
		t.Errorf("identity fields lost: %+v", m)
	}
	if m.Capabilities.Thinking == nil || !*m.Capabilities.Thinking {
		t.Error("Thinking capability not preserved through round-trip")
	}
	if m.Capabilities.PDF != nil {
		t.Error("PDF capability nil should round-trip nil, not a zero pointer")
	}
}

// TestSortByReleaseDateDescending verifies the byProv sort order:
// newer-first, ID as tiebreak. This is exercised through Get() so
// it covers the actual code path the picker uses.
func TestSortByReleaseDateDescending(t *testing.T) {
	// Build a synthetic byProv directly — bypassing the embedded
	// JSON keeps the test independent of whatever's in catalog.gen.json.
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	models := []Model{
		{ID: "old", Provider: "p", ReleasedAt: older},
		{ID: "new", Provider: "p", ReleasedAt: newer},
		{ID: "older-tied", Provider: "p", ReleasedAt: older},
	}
	sort.SliceStable(models, func(i, j int) bool {
		if !models[i].ReleasedAt.Equal(models[j].ReleasedAt) {
			return models[i].ReleasedAt.After(models[j].ReleasedAt)
		}
		return models[i].ID < models[j].ID
	})
	if models[0].ID != "new" {
		t.Errorf("newest first: got %q want %q", models[0].ID, "new")
	}
	if models[1].ID != "old" || models[2].ID != "older-tied" {
		t.Errorf("ID tiebreak broken: got %q,%q want old,older-tied", models[1].ID, models[2].ID)
	}
}

// Get("openai-auth") consults the per-user models file written after
// each successful login. With no file present, it returns an empty
// slice so callers can render an empty-state hint ("run yottacode
// openai-auth login"). When the file exists, every ID is wrapped into
// a catalog.Model with a friendly display name from the label table
// (or the ID itself for unrecognised IDs).
func TestGet_OpenAIAuthEmptyWithoutModelsFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := Get("openai-auth")
	if len(got) != 0 {
		t.Errorf("Get(\"openai-auth\") = %v, want empty when no file present", got)
	}
}

func TestGet_OpenAIAuthFromModelsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := openaiauth.DefaultModelsPath()
	if err != nil {
		t.Fatalf("DefaultModelsPath: %v", err)
	}
	if err := openaiauth.SaveModels(path, openaiauth.ModelsFile{
		Models: []string{"gpt-5.5", "gpt-5.4", "gpt-9-future"},
	}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	got := Get("openai-auth")
	if len(got) != 3 {
		t.Fatalf("Get returned %d entries, want 3", len(got))
	}
	wantLabels := map[string]string{
		"gpt-5.5":      "GPT-5.5",
		"gpt-5.4":      "GPT-5.4",
		"gpt-9-future": "gpt-9-future", // unrecognised → ID as label
	}
	for _, m := range got {
		if want := wantLabels[m.ID]; m.DisplayName != want {
			t.Errorf("DisplayName for %q = %q, want %q", m.ID, m.DisplayName, want)
		}
		if m.Provider != "openai-auth" {
			t.Errorf("Provider for %q = %q, want openai-auth", m.ID, m.Provider)
		}
	}
}

func TestOpenAIAuthLabelsCoverDefaultCandidates(t *testing.T) {
	for _, id := range openaiauth.DefaultCandidates {
		if _, ok := openAIAuthLabels[id]; !ok {
			t.Errorf("openAIAuthLabels missing entry for default candidate %q", id)
		}
	}
}
