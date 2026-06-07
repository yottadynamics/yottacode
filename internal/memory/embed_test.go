package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbed_Success(t *testing.T) {
	expected := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "nomic-embed-text" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		json.NewEncoder(w).Encode(embedResponse{Embedding: expected})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	got, err := client.Embed(context.Background(), "test text")
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("expected %d dimensions; got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("dimension %d: got %f, want %f", i, got[i], expected[i])
		}
	}
}

// TestEmbed_SendsKeepAlive pins the keep_alive request hint: without
// it Ollama evicts the embedding model after ~5min idle, and the next
// per-turn retrieval pays a multi-second cold load (the source of the
// "first Enter after a pause" stall the retrieval move fixed).
func TestEmbed_SendsKeepAlive(t *testing.T) {
	var got embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(embedResponse{Embedding: []float32{1}})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "m")
	if _, err := client.Embed(context.Background(), "text"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got.KeepAlive != embedKeepAlive {
		t.Errorf("keep_alive = %q, want %q", got.KeepAlive, embedKeepAlive)
	}
}

func TestEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	_, err := client.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestEmbed_EmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embedResponse{Embedding: nil})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	_, err := client.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error on empty embedding")
	}
}

func TestAvailable_ModelFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "llama3.1:8b"},
				{"name": "nomic-embed-text:latest"},
			},
		})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	if !client.Available(context.Background()) {
		t.Error("Available should return true when model is installed")
	}
}

func TestAvailable_ModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "llama3.1:8b"},
			},
		})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	if client.Available(context.Background()) {
		t.Error("Available should return false when model is not installed")
	}
}

func TestAvailable_ServerDown(t *testing.T) {
	client := NewEmbedClient("http://localhost:1", "nomic-embed-text")
	if client.Available(context.Background()) {
		t.Error("Available should return false when server is down")
	}
}

func TestNewEmbedClient_StripV1Suffix(t *testing.T) {
	client := NewEmbedClient("http://localhost:11434/v1", "test-model")
	if client.BaseURL != "http://localhost:11434" {
		t.Errorf("expected /v1 stripped; got %s", client.BaseURL)
	}
}

func TestStatus_ReachableAndInstalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "nomic-embed-text:latest"},
			},
		})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	reachable, installed := client.Status(context.Background())
	if !reachable || !installed {
		t.Errorf("Status() = (reachable=%v, installed=%v), want (true, true)", reachable, installed)
	}
}

func TestStatus_ReachableButModelMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "llama3.1:8b"},
			},
		})
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL, "nomic-embed-text")
	reachable, installed := client.Status(context.Background())
	if !reachable {
		t.Errorf("Status() reachable should be true, got false")
	}
	if installed {
		t.Errorf("Status() installed should be false, got true")
	}
}

func TestStatus_OllamaDown(t *testing.T) {
	client := NewEmbedClient("http://localhost:1", "nomic-embed-text")
	reachable, installed := client.Status(context.Background())
	if reachable || installed {
		t.Errorf("Status() = (reachable=%v, installed=%v), want (false, false)", reachable, installed)
	}
}
