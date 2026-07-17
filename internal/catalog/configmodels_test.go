package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
	"github.com/yottadynamics/yottacode/internal/config"
)

// A non-curated endpoint that implements chat/completions but no /models
// route is common — Vertex's Gemini shim 404s on it. Before this fix
// List went straight to Live and never looked at p.Models, so a user who
// had hand-declared their models still got an empty picker and an error.
func TestList_ConfigModelsRescueAFailedLiveFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	p := config.Provider{
		Name:    "shim",
		Kind:    "openai-compatible",
		BaseURL: srv.URL,
		Models: []config.Model{
			{Name: "google/gemini-2.5-pro", ContextWindow: 1048576},
			{Name: "google/gemini-2.5-flash"},
		},
	}

	got, err := List(context.Background(), p, "")
	// The error still travels — the picker notes the failed fetch under
	// the list and `model fetch` warns — but it must not cost the user
	// the models they declared.
	if err == nil {
		t.Error("live-fetch failure was swallowed; an unreachable endpoint would look healthy")
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d models, want the 2 declared in config", len(got))
	}
	if got[0].ID != "google/gemini-2.5-pro" {
		t.Errorf("model[0].ID = %q, want the declared id verbatim", got[0].ID)
	}
	if got[0].ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want the declared 1048576", got[0].ContextWindow)
	}
	if got[0].Provider != "openai-compatible" {
		t.Errorf("Provider = %q, want the profile's kind", got[0].Provider)
	}
}

// With no declared models there is nothing to fall back on, so a broken
// endpoint must still surface its error — for Ollama especially, a failed
// probe usually means the daemon isn't running and the user needs to know.
func TestList_LiveErrorStillSurfacesWithoutConfigModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := List(context.Background(), config.Provider{
		Name:    "shim",
		Kind:    "openai-compatible",
		BaseURL: srv.URL,
	}, "")
	if err == nil {
		t.Fatal("List swallowed a live-fetch failure that it cannot compensate for")
	}
}

func TestList_ConfigModelsMergeWithLiveResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"served-by-endpoint"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := List(context.Background(), config.Provider{
		Name:    "proxy",
		Kind:    "openai-compatible",
		BaseURL: srv.URL,
		Models:  []config.Model{{Name: "hand-declared"}},
	}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasModelID(got, "served-by-endpoint") {
		t.Error("live-fetched model missing from the merged list")
	}
	if !hasModelID(got, "hand-declared") {
		t.Error("config-declared model missing from the merged list")
	}
}

// Curated kinds take their list from the catalog, but a user who adds a
// model the catalog lags on should still see it.
func TestList_ConfigModelsMergeIntoCuratedKinds(t *testing.T) {
	got, err := List(context.Background(), config.Provider{
		Name:   "vertex-claude",
		Kind:   "vertex-anthropic",
		Models: []config.Model{{Name: "claude-from-the-future@20990101"}},
	}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasModelID(got, "claude-from-the-future@20990101") {
		t.Error("config-declared model missing from a curated kind's list")
	}
	if !hasModelID(got, "claude-sonnet-4-5@20250929") {
		t.Error("curated models missing after merging declared ones")
	}
}

// The catalog entry wins on a duplicate id: it carries real limits and a
// display name, where the config entry is just a name.
func TestList_CuratedEntryWinsOverDuplicateConfigEntry(t *testing.T) {
	got, err := List(context.Background(), config.Provider{
		Name:   "vertex-gemini",
		Kind:   "vertex",
		Models: []config.Model{{Name: "google/gemini-2.5-pro", ContextWindow: 42}},
	}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if m.ID == "google/gemini-2.5-pro" && m.ContextWindow == 42 {
			t.Error("config entry shadowed the curated one; the catalog's real limits should win")
		}
	}
}

func TestList_VertexAccessScanMatchesBareGeminiAliases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi"
	_, _, err := vertexauth.PersistScan(vertexauth.ProviderVertex, baseURL, nil, []vertexauth.ScanResult{
		{Name: "google/gemini-2.5-pro", Status: "404", Access: vertexauth.AccessDenied},
	})
	if err != nil {
		t.Fatalf("PersistScan: %v", err)
	}

	got, err := List(context.Background(), config.Provider{
		Name:    "vertex-gemini",
		Kind:    "vertex",
		BaseURL: baseURL,
		Models:  []config.Model{{Name: "gemini-2.5-pro"}},
	}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if m.ID == "gemini-2.5-pro" && !m.Disabled {
			t.Fatal("bare gemini-2.5-pro stayed enabled despite denied google/gemini-2.5-pro scan result")
		}
	}
}
