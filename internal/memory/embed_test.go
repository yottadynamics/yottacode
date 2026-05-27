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
