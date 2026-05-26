package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenSource_Token(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "copilot.json")
	if err := Save(storePath, TokenSet{GitHubToken: "gho_test"}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_copilot_token",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints": map[string]string{
				"api": "https://api.githubcopilot.com",
			},
		})
	}))
	defer ts.Close()

	src := NewTokenSource(storePath)
	src.SetHTTPClient(ts.Client())
	src.SetTokenEndpoint(ts.URL)

	token, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "tid_copilot_token" {
		t.Errorf("token = %q, want %q", token, "tid_copilot_token")
	}

	// Second call should return cached token (no HTTP request)
	token2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if token2 != "tid_copilot_token" {
		t.Errorf("cached token = %q, want %q", token2, "tid_copilot_token")
	}
}

func TestTokenSource_NotLoggedIn(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "copilot.json")

	src := NewTokenSource(storePath)
	_, err := src.Token(context.Background())
	if err == nil {
		t.Fatal("expected error when not logged in")
	}
}

func TestTokenSource_ForceRefresh(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "copilot.json")
	if err := Save(storePath, TokenSet{GitHubToken: "gho_test"}); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_refreshed",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": "https://api.githubcopilot.com"},
		})
	}))
	defer ts.Close()

	src := NewTokenSource(storePath)
	src.SetHTTPClient(ts.Client())
	src.SetTokenEndpoint(ts.URL)

	// First call
	_, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", callCount)
	}

	// Force refresh
	if err := src.ForceRefresh(context.Background()); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 HTTP calls after ForceRefresh, got %d", callCount)
	}
}

func TestTokenSource_APIEndpoint(t *testing.T) {
	src := NewTokenSource("/nonexistent")
	if ep := src.APIEndpoint(); ep != CopilotAPIEndpoint {
		t.Errorf("default endpoint = %q, want %q", ep, CopilotAPIEndpoint)
	}
}
