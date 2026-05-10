package wizard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSnapshotEnv: sets a known key in the test env, expects the
// SuggestedProviders list to contain the matching catalog name.
func TestSnapshotEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	// Make sure other env keys don't bleed into this test's
	// expectation by clearing them.
	for _, k := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "XAI_API_KEY", "NVIDIA_API_KEY"} {
		t.Setenv(k, "")
	}
	snap := SnapshotEnv()
	if !snap.Present["ANTHROPIC_API_KEY"] {
		t.Errorf("Present should include ANTHROPIC_API_KEY")
	}
	hit := false
	for _, p := range snap.SuggestedProviders {
		if p == "anthropic" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("SuggestedProviders should include anthropic, got %v", snap.SuggestedProviders)
	}
}

// TestProbeOllamaSuccess uses an httptest server to fake /api/tags.
func TestProbeOllamaSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"llama3.1:8b"},{"name":"qwen3.5:latest"}]}`))
	}))
	defer srv.Close()

	res := ProbeOllama(context.Background(), srv.URL)
	if !res.Reachable {
		t.Fatalf("expected reachable; err=%v", res.Err)
	}
	if len(res.Models) != 2 || res.Models[0] != "llama3.1:8b" {
		t.Errorf("unexpected models %v", res.Models)
	}
}

// TestProbeOllamaFailure: a closed listener should produce a
// not-reachable result quickly without blocking the test.
func TestProbeOllamaFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately closed
	start := time.Now()
	res := ProbeOllama(context.Background(), srv.URL)
	if res.Reachable {
		t.Errorf("closed server should not be reachable")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("probe took too long: %v", elapsed)
	}
}

// TestValidateKey covers all four status outcomes by driving the
// underlying provider URL through httptest.
func TestValidateKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models", "/models":
			auth := r.Header.Get("Authorization")
			if auth == "Bearer good" || r.Header.Get("x-api-key") == "good" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	openai := CatalogEntry{Name: "openai", Kind: "openai", BaseURL: srv.URL}
	anth := CatalogEntry{Name: "anthropic", Kind: "anthropic", BaseURL: srv.URL}

	if r := ValidateKey(context.Background(), openai, "good"); r.Status != ValidationOK {
		t.Errorf("openai good key: status %v", r.Status)
	}
	if r := ValidateKey(context.Background(), openai, "bad"); r.Status != ValidationFailed {
		t.Errorf("openai bad key: status %v", r.Status)
	}
	if r := ValidateKey(context.Background(), anth, "good"); r.Status != ValidationOK {
		t.Errorf("anthropic good key: status %v", r.Status)
	}
	if r := ValidateKey(context.Background(), anth, ""); r.Status != ValidationUnknown {
		t.Errorf("anthropic empty key: status %v", r.Status)
	}
	ollama := CatalogEntry{Name: "ollama", Kind: "ollama"}
	if r := ValidateKey(context.Background(), ollama, "anything"); r.Status != ValidationUnknown {
		t.Errorf("ollama: status %v (should be unknown — no validation)", r.Status)
	}
}
