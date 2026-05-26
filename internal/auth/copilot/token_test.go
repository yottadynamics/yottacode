package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchCopilotToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "token ") {
			t.Errorf("expected 'token ' prefix in Authorization, got %q", auth)
		}
		if auth != "token gho_test" {
			t.Errorf("Authorization = %q, want %q", auth, "token gho_test")
		}
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

	ct, err := FetchCopilotTokenFrom(context.Background(), ts.Client(), "gho_test", ts.URL)
	if err != nil {
		t.Fatalf("FetchCopilotTokenFrom: %v", err)
	}
	if ct.Token != "tid_copilot_token" {
		t.Errorf("token = %q, want %q", ct.Token, "tid_copilot_token")
	}
	if ct.Endpoints.API != "https://api.githubcopilot.com" {
		t.Errorf("api endpoint = %q, want %q", ct.Endpoints.API, "https://api.githubcopilot.com")
	}
}

func TestFetchCopilotToken_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ts.Close()

	_, err := FetchCopilotTokenFrom(context.Background(), ts.Client(), "bad_token", ts.URL)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
}

func TestFetchCopilotToken_Forbidden(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"No Copilot subscription"}`))
	}))
	defer ts.Close()

	_, err := FetchCopilotTokenFrom(context.Background(), ts.Client(), "no_sub_token", ts.URL)
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got: %v", err)
	}
}

func TestFetchCopilotToken_DefaultEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_test",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),

			"endpoints":  map[string]string{},
		})
	}))
	defer ts.Close()

	ct, err := FetchCopilotTokenFrom(context.Background(), ts.Client(), "gho_test", ts.URL)
	if err != nil {
		t.Fatalf("FetchCopilotTokenFrom: %v", err)
	}
	if ct.Endpoints.API != CopilotAPIEndpoint {
		t.Errorf("expected default API endpoint %q, got %q", CopilotAPIEndpoint, ct.Endpoints.API)
	}
}
