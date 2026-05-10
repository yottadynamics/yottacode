package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetry sets retryDelay to 0 for the lifetime of t. Without this
// the 1.5s production delay multiplied by maxAttempts blows up the
// test wall time.
func fastRetry(t *testing.T) {
	t.Helper()
	prev := retryDelay
	retryDelay = 0
	t.Cleanup(func() { retryDelay = prev })
}

// scanServer builds an httptest server whose response is decided by
// fn — the function gets the JSON-decoded model name and returns the
// status code + raw body to write. Lets each test write a tiny per-
// model decision table without re-implementing the request framing.
func scanServer(t *testing.T, fn func(model string) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing Bearer auth, got %q", got)
		}
		if got := r.Header.Get("originator"); got == "" {
			t.Errorf("missing originator header")
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
			t.Errorf("OpenAI-Beta = %q, want responses=experimental", got)
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Store  bool   `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if !body.Stream {
			t.Errorf("stream must be true")
		}
		if body.Store {
			t.Errorf("store must be false")
		}
		status, payload := fn(body.Model)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
}

func TestScanWithToken_HappyPath(t *testing.T) {
	fastRetry(t)
	srv := scanServer(t, func(model string) (int, string) {
		return http.StatusOK, `{"ok":true}`
	})
	defer srv.Close()

	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.5", "gpt-5.4"},
	})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].OK || results[0].Name != "gpt-5.5" {
		t.Errorf("results[0] = %+v", results[0])
	}
	if !results[1].OK || results[1].Name != "gpt-5.4" {
		t.Errorf("results[1] = %+v", results[1])
	}
}

func TestScanWithToken_429TreatedAsAvailable(t *testing.T) {
	fastRetry(t)
	var calls int32
	srv := scanServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusTooManyRequests, `{"detail":"You have hit your usage limit on this model. Try again later."}`
	})
	defer srv.Close()

	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.4"},
	})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !results[0].OK {
		t.Errorf("429 should be treated as available, got %+v", results[0])
	}
	if results[0].Status != "429" {
		t.Errorf("Status = %q, want 429 (preserves origin code)", results[0].Status)
	}
	if !strings.Contains(results[0].Detail, "usage limit") {
		t.Errorf("Detail should carry the server's reason, got %q", results[0].Detail)
	}
	// 429 should NOT retry — the model is available, retrying just
	// burns quota the user is already out of.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("429 must not retry, got %d calls", got)
	}
}

func TestScanWithToken_4xxNoRetry(t *testing.T) {
	fastRetry(t)
	var calls int32
	srv := scanServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusForbidden, `{"detail":"not in your plan"}`
	})
	defer srv.Close()

	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.4"},
	})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].OK {
		t.Errorf("expected OK=false, got %+v", results[0])
	}
	if results[0].Status != "403" {
		t.Errorf("Status = %q, want 403", results[0].Status)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("4xx must not retry, got %d calls", got)
	}
	if !strings.Contains(results[0].Detail, "not in your plan") {
		t.Errorf("Detail = %q, want contains 'not in your plan'", results[0].Detail)
	}
}

func TestScanWithToken_5xxRetriesUpTo3(t *testing.T) {
	fastRetry(t)
	var calls int32
	srv := scanServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusServiceUnavailable, `{"detail":"backend down"}`
	})
	defer srv.Close()

	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.5"},
	})
	if results[0].OK {
		t.Errorf("expected OK=false after retries")
	}
	if got := atomic.LoadInt32(&calls); got != int32(maxAttempts) {
		t.Errorf("expected %d attempts, got %d", maxAttempts, got)
	}
	if results[0].Status != "503" {
		t.Errorf("Status = %q, want 503", results[0].Status)
	}
}

func TestScanWithToken_5xxThenSuccess(t *testing.T) {
	fastRetry(t)
	var calls int32
	srv := scanServer(t, func(model string) (int, string) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return http.StatusBadGateway, `{"error":{"message":"transient"}}`
		}
		return http.StatusOK, `{"ok":true}`
	})
	defer srv.Close()

	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.5"},
	})
	if !results[0].OK {
		t.Errorf("expected OK after retry, got %+v", results[0])
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
}

func TestScanWithToken_NetworkErrorRetries(t *testing.T) {
	fastRetry(t)
	results := ScanWithToken(context.Background(), "tkn", ScanOptions{
		// 127.0.0.1:1 is reliably unreachable; the scanner will see
		// connection-refused on every attempt.
		Endpoint:   "http://127.0.0.1:1/never",
		Candidates: []string{"gpt-5.5"},
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	})
	if results[0].OK {
		t.Errorf("expected OK=false on unreachable endpoint")
	}
	if results[0].Status != "ERR" {
		t.Errorf("Status = %q, want ERR", results[0].Status)
	}
}

func TestScanWithToken_ContextCancelStopsLoop(t *testing.T) {
	fastRetry(t)
	srv := scanServer(t, func(model string) (int, string) {
		return http.StatusOK, `{"ok":true}`
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := ScanWithToken(ctx, "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.5", "gpt-5.4"},
	})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (one per candidate even when cancelled)", len(results))
	}
	for _, r := range results {
		if r.OK {
			t.Errorf("expected all OK=false on cancelled ctx, got %+v", r)
		}
	}
}

func TestOKNamesPreservesOrder(t *testing.T) {
	results := []ScanResult{
		{Name: "gpt-5.5", OK: true},
		{Name: "gpt-5.4", OK: false},
		{Name: "gpt-5.4-mini", OK: true},
	}
	got := OKNames(results)
	want := []string{"gpt-5.5", "gpt-5.4-mini"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("OKNames = %v, want %v", got, want)
	}
}

func TestScanAndPersist_WritesFileOnSuccess(t *testing.T) {
	fastRetry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := scanServer(t, func(model string) (int, string) {
		if model == "gpt-5.4" {
			return http.StatusForbidden, `{"detail":"upgrade plan"}`
		}
		return http.StatusOK, `{"ok":true}`
	})
	defer srv.Close()

	models, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{
		Endpoint:   srv.URL,
		Candidates: []string{"gpt-5.5", "gpt-5.4"},
	})
	if err != nil {
		t.Fatalf("ScanAndPersistWithOptions: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	path := filepath.Join(home, ".yottacode", "auth", "openai-auth-models.json")
	mf, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(mf.Models) != len(models) {
		t.Errorf("file Models = %v, returned = %v", mf.Models, models)
	}
	if mf.ScannedAt.IsZero() {
		t.Error("ScannedAt should be set")
	}
	if len(mf.Candidates) == 0 {
		t.Error("Candidates should be populated")
	}
	// Results carries the full per-candidate breakdown, including the
	// rejected ones. This is the field a user inspects when auditing
	// "why was gpt-X marked available?"
	if len(mf.Results) != 2 {
		t.Fatalf("Results = %v, want 2 entries (one per candidate, accepted or not)", mf.Results)
	}
	var sawOK, sawForbidden bool
	for _, r := range mf.Results {
		switch r.Name {
		case "gpt-5.5":
			if !r.OK {
				t.Errorf("gpt-5.5 should be OK: %+v", r)
			}
			sawOK = true
		case "gpt-5.4":
			if r.OK || r.Status != "403" {
				t.Errorf("gpt-5.4 should be 403/non-OK: %+v", r)
			}
			if !strings.Contains(r.Detail, "upgrade plan") {
				t.Errorf("gpt-5.4 Detail should preserve server message: %q", r.Detail)
			}
			sawForbidden = true
		}
	}
	if !sawOK || !sawForbidden {
		t.Errorf("Results missing one of {ok, forbidden}: %+v", mf.Results)
	}
}

func TestScanAndPersist_NoFileOnZeroOK(t *testing.T) {
	fastRetry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := scanServer(t, func(model string) (int, string) {
		return http.StatusForbidden, `{"detail":"none of these"}`
	})
	defer srv.Close()

	_, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrNoModelsAvailable) {
		t.Errorf("got %v, want ErrNoModelsAvailable", err)
	}

	path := filepath.Join(home, ".yottacode", "auth", "openai-auth-models.json")
	if _, err := LoadModels(path); !errors.Is(err, ErrModelsNotFound) {
		t.Errorf("expected no file, got LoadModels err = %v", err)
	}
}

func TestScanAndPersist_PreservesPriorFileOnFailure(t *testing.T) {
	fastRetry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed a prior good scan.
	priorPath := filepath.Join(home, ".yottacode", "auth", "openai-auth-models.json")
	if err := SaveModels(priorPath, ModelsFile{
		ScannedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Models:    []string{"gpt-5.5", "gpt-5.4"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := scanServer(t, func(model string) (int, string) {
		return http.StatusForbidden, `{"detail":"none today"}`
	})
	defer srv.Close()

	if _, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{Endpoint: srv.URL}); !errors.Is(err, ErrNoModelsAvailable) {
		t.Errorf("got %v, want ErrNoModelsAvailable", err)
	}

	mf, err := LoadModels(priorPath)
	if err != nil {
		t.Fatalf("prior file should still load: %v", err)
	}
	if len(mf.Models) != 2 {
		t.Errorf("prior Models clobbered: got %v, want [gpt-5.5 gpt-5.4]", mf.Models)
	}
}
