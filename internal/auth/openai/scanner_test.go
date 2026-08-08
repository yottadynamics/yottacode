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

func catalogBody(models ...remoteModel) string {
	b, _ := json.Marshal(modelsCatalogResponse{Models: models})
	return string(b)
}

// modelsServer serves a fixed model catalog off the GET endpoint.
func modelsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing Bearer auth, got %q", got)
		}
		if got := r.URL.Query().Get("client_version"); got == "" {
			t.Errorf("missing client_version query param")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

// probeServer builds an httptest server whose response is decided by
// fn — the function gets the JSON-decoded model name and returns the
// status code + raw body to write. Lets each test write a tiny
// per-model decision table without re-implementing the request
// framing for the POST /responses probe.
func probeServer(t *testing.T, fn func(model string) (int, string)) *httptest.Server {
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

func TestFetchModels_FiltersAndSortsByPriority(t *testing.T) {
	srv := modelsServer(t, catalogBody(
		remoteModel{Slug: "gpt-5.4", DisplayName: "GPT-5.4", Visibility: "list", SupportedInAPI: true, Priority: 16},
		remoteModel{Slug: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", Visibility: "list", SupportedInAPI: true, Priority: 1},
		remoteModel{Slug: "gpt-5.6-sol-wm", DisplayName: "GPT-5.6-Sol-WM", Visibility: "hide", SupportedInAPI: false, Priority: 1},
		remoteModel{Slug: "gpt-5.3-codex-spark", DisplayName: "GPT-5.3-Codex-Spark", Visibility: "list", SupportedInAPI: false, Priority: 26},
		remoteModel{Slug: "codex-auto-review", DisplayName: "Auto Review", Visibility: "hide", SupportedInAPI: true, Priority: 43},
	))
	defer srv.Close()

	got, err := FetchModels(context.Background(), "tkn", ScanOptions{ModelsEndpoint: srv.URL})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	want := []FetchedModel{
		{Slug: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol"},
		{Slug: "gpt-5.4", DisplayName: "GPT-5.4"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestFetchModels_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"session expired"}`))
	}))
	defer srv.Close()

	_, err := FetchModels(context.Background(), "tkn", ScanOptions{ModelsEndpoint: srv.URL})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("error should carry server detail, got %q", err.Error())
	}
}

func TestFetchModels_DecodeError(t *testing.T) {
	srv := modelsServer(t, "not json")
	defer srv.Close()

	_, err := FetchModels(context.Background(), "tkn", ScanOptions{ModelsEndpoint: srv.URL})
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFetchModels_NetworkError(t *testing.T) {
	_, err := FetchModels(context.Background(), "tkn", ScanOptions{
		// 127.0.0.1:1 is reliably unreachable.
		ModelsEndpoint: "http://127.0.0.1:1/never",
		HTTPClient:     &http.Client{Timeout: 200 * time.Millisecond},
	})
	if err == nil {
		t.Fatal("expected network error")
	}
}

// oneCandidateModelsServer is the common case in the probe tests
// below: the catalog offers exactly one candidate, and the test cares
// about how ScanWithToken's probe step handles it.
func oneCandidateModelsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	return modelsServer(t, catalogBody(
		remoteModel{Slug: slug, DisplayName: strings.ToUpper(slug), Visibility: "list", SupportedInAPI: true, Priority: 1},
	))
}

func TestScanWithToken_HappyPath(t *testing.T) {
	fastRetry(t)
	catalog := modelsServer(t, catalogBody(
		remoteModel{Slug: "gpt-5.5", DisplayName: "GPT-5.5", Visibility: "list", SupportedInAPI: true, Priority: 7},
		remoteModel{Slug: "gpt-5.4", DisplayName: "GPT-5.4", Visibility: "list", SupportedInAPI: true, Priority: 16},
	))
	defer catalog.Close()
	probe := probeServer(t, func(model string) (int, string) {
		return http.StatusOK, `{"ok":true}`
	})
	defer probe.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
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

func TestScanWithToken_CatalogFetchErrorAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := ScanWithToken(context.Background(), "tkn", ScanOptions{ModelsEndpoint: srv.URL})
	if err == nil {
		t.Fatal("expected error when the catalog fetch itself fails")
	}
}

func TestScanWithToken_429TreatedAsAvailable(t *testing.T) {
	fastRetry(t)
	catalog := oneCandidateModelsServer(t, "gpt-5.4")
	defer catalog.Close()
	var calls int32
	probe := probeServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusTooManyRequests, `{"detail":"You have hit your usage limit on this model. Try again later."}`
	})
	defer probe.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
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
	catalog := oneCandidateModelsServer(t, "gpt-5.4")
	defer catalog.Close()
	var calls int32
	probe := probeServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusForbidden, `{"detail":"not in your plan"}`
	})
	defer probe.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
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
	catalog := oneCandidateModelsServer(t, "gpt-5.5")
	defer catalog.Close()
	var calls int32
	probe := probeServer(t, func(model string) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusServiceUnavailable, `{"detail":"backend down"}`
	})
	defer probe.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
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
	catalog := oneCandidateModelsServer(t, "gpt-5.5")
	defer catalog.Close()
	var calls int32
	probe := probeServer(t, func(model string) (int, string) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return http.StatusBadGateway, `{"error":{"message":"transient"}}`
		}
		return http.StatusOK, `{"ok":true}`
	})
	defer probe.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
	if !results[0].OK {
		t.Errorf("expected OK after retry, got %+v", results[0])
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
}

func TestScanWithToken_NetworkErrorRetries(t *testing.T) {
	fastRetry(t)
	catalog := oneCandidateModelsServer(t, "gpt-5.5")
	defer catalog.Close()

	results, err := ScanWithToken(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		// 127.0.0.1:1 is reliably unreachable; the probe will see
		// connection-refused on every attempt.
		ProbeEndpoint: "http://127.0.0.1:1/never",
		HTTPClient:    &http.Client{Timeout: 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("ScanWithToken: %v", err)
	}
	if results[0].OK {
		t.Errorf("expected OK=false on unreachable endpoint")
	}
	if results[0].Status != "ERR" {
		t.Errorf("Status = %q, want ERR", results[0].Status)
	}
}

func TestScanWithToken_ContextCancelStopsLoop(t *testing.T) {
	fastRetry(t)
	catalog := modelsServer(t, catalogBody(
		remoteModel{Slug: "gpt-5.5", Visibility: "list", SupportedInAPI: true, Priority: 1},
		remoteModel{Slug: "gpt-5.4", Visibility: "list", SupportedInAPI: true, Priority: 2},
	))
	defer catalog.Close()
	probe := probeServer(t, func(model string) (int, string) {
		return http.StatusOK, `{"ok":true}`
	})
	defer probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// FetchModels itself needs the context alive; cancel only after
	// the candidate list is in hand isn't possible from the caller's
	// side, so this exercises the same "cancelled before any probe
	// runs" path the original test covered by cancelling up front —
	// FetchModels will fail fast on a cancelled context too.
	cancel()
	_, err := ScanWithToken(ctx, "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err == nil {
		t.Fatal("expected error: catalog fetch should fail on a cancelled context")
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

func TestOKDisplayNames(t *testing.T) {
	results := []ScanResult{
		{Name: "gpt-5.5", DisplayName: "GPT-5.5", OK: true},
		{Name: "gpt-5.4", DisplayName: "GPT-5.4", OK: false},
		{Name: "gpt-5.4-mini", OK: true}, // OK but no display name
	}
	got := OKDisplayNames(results)
	if got["gpt-5.5"] != "GPT-5.5" {
		t.Errorf("DisplayNames[gpt-5.5] = %q, want GPT-5.5", got["gpt-5.5"])
	}
	if _, ok := got["gpt-5.4"]; ok {
		t.Errorf("gpt-5.4 is not OK, should not appear in DisplayNames")
	}
	if _, ok := got["gpt-5.4-mini"]; ok {
		t.Errorf("gpt-5.4-mini has no display name, should not appear in DisplayNames")
	}
}

func TestScanAndPersist_WritesFileOnSuccess(t *testing.T) {
	fastRetry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	catalog := modelsServer(t, catalogBody(
		remoteModel{Slug: "gpt-5.5", DisplayName: "GPT-5.5", Visibility: "list", SupportedInAPI: true, Priority: 7},
		remoteModel{Slug: "gpt-5.4", DisplayName: "GPT-5.4", Visibility: "list", SupportedInAPI: true, Priority: 16},
	))
	defer catalog.Close()
	probe := probeServer(t, func(model string) (int, string) {
		if model == "gpt-5.4" {
			return http.StatusForbidden, `{"detail":"upgrade plan"}`
		}
		return http.StatusOK, `{"ok":true}`
	})
	defer probe.Close()

	models, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
	if err != nil {
		t.Fatalf("ScanAndPersistWithOptions: %v", err)
	}
	if len(models) != 1 || models[0] != "gpt-5.5" {
		t.Errorf("models = %v, want [gpt-5.5]", models)
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
	if mf.DisplayNames["gpt-5.5"] != "GPT-5.5" {
		t.Errorf("DisplayNames[gpt-5.5] = %q, want GPT-5.5", mf.DisplayNames["gpt-5.5"])
	}
	// gpt-5.4 was rejected by the per-account probe (403) even though
	// the catalog listed it as generically API-shaped — it must not
	// leak into the persisted file.
	if _, ok := mf.DisplayNames["gpt-5.4"]; ok {
		t.Error("gpt-5.4 was 403'd by the probe, should not be persisted")
	}
}

func TestScanAndPersist_NoFileOnZeroOK(t *testing.T) {
	fastRetry(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	catalog := oneCandidateModelsServer(t, "gpt-5.5")
	defer catalog.Close()
	probe := probeServer(t, func(model string) (int, string) {
		return http.StatusForbidden, `{"detail":"none of these"}`
	})
	defer probe.Close()

	_, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	})
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

	catalog := oneCandidateModelsServer(t, "gpt-5.5")
	defer catalog.Close()
	probe := probeServer(t, func(model string) (int, string) {
		return http.StatusForbidden, `{"detail":"none today"}`
	})
	defer probe.Close()

	if _, err := ScanAndPersistWithOptions(context.Background(), "tkn", ScanOptions{
		ModelsEndpoint: catalog.URL,
		ProbeEndpoint:  probe.URL,
	}); !errors.Is(err, ErrNoModelsAvailable) {
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
