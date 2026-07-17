package vertex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticTokenSource struct{ token string }

func (s staticTokenSource) Token(context.Context) (string, error) { return s.token, nil }

func TestProjectLocation(t *testing.T) {
	project, location := ProjectLocation("https://aiplatform.googleapis.com/v1/projects/my-proj/locations/global/endpoints/openapi")
	if project != "my-proj" || location != "global" {
		t.Fatalf("ProjectLocation = (%q, %q), want (my-proj, global)", project, location)
	}
}

func TestScanVertexAnthropicMarksUnavailableModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ya29.test" {
			t.Errorf("Authorization = %q, want ADC bearer", got)
		}
		switch {
		case strings.Contains(r.URL.Path, "claude-sonnet-4-5@20250929:rawPredict"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.Contains(r.URL.Path, "claude-opus-4-8@default:rawPredict"):
			http.Error(w, `{"error":{"status":"NOT_FOUND","message":"Publisher model not found or no access"}}`, http.StatusNotFound)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	results, err := Scan(context.Background(), ScanOptions{
		HTTPClient:  srv.Client(),
		Provider:    ProviderVertexAnthropic,
		BaseURL:     srv.URL + "/v1/projects/p/locations/global",
		Candidates:  []string{"claude-sonnet-4-5@20250929", "claude-opus-4-8@default"},
		TokenSource: staticTokenSource{token: "ya29.test"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].OK {
		t.Errorf("sonnet result = %+v, want OK", results[0])
	}
	if results[1].OK || results[1].Status != "404" || !strings.Contains(results[1].Detail, "NOT_FOUND") {
		t.Errorf("opus result = %+v, want 404 unavailable detail", results[1])
	}
}

// Regression: a transient failure (here a 5xx) must not be recorded as a
// denial. Before the fix, any non-200/429 marked the model OK=false and
// AccessMap turned that into a hard "unavailable", greying out a model
// that a retry would have reached.
func TestScan_TransientFailureIsUnknownNotDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"status":"INTERNAL","message":"backend error"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	results, err := Scan(context.Background(), ScanOptions{
		HTTPClient:  srv.Client(),
		Provider:    ProviderVertexAnthropic,
		BaseURL:     srv.URL + "/v1/projects/p/locations/global",
		Candidates:  []string{"claude-sonnet-4-5@20250929"},
		TokenSource: staticTokenSource{token: "ya29.test"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := results[0].EffectiveAccess(); got != AccessUnknown {
		t.Fatalf("500 response classified as %q, want %q", got, AccessUnknown)
	}
	if results[0].OK {
		t.Error("a 500 must not count as available")
	}
}

// Only 403 and 404 are definitive per-model denials.
func TestScan_ForbiddenAndNotFoundAreDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "denied-403") {
			http.Error(w, `{"error":{"status":"PERMISSION_DENIED"}}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":{"status":"NOT_FOUND"}}`, http.StatusNotFound)
	}))
	defer srv.Close()

	results, err := Scan(context.Background(), ScanOptions{
		HTTPClient:  srv.Client(),
		Provider:    ProviderVertexAnthropic,
		BaseURL:     srv.URL + "/v1/projects/p/locations/global",
		Candidates:  []string{"denied-403@default", "missing-404@default"},
		TokenSource: staticTokenSource{token: "ya29.test"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, r := range results {
		if got := r.EffectiveAccess(); got != AccessDenied {
			t.Errorf("%s classified as %q, want denied", r.Name, got)
		}
	}
}

// A scan cancelled partway through must leave the un-probed models
// unknown, not persist them as denied.
func TestScan_CancelledContextLeavesRemainingUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the loop body runs

	results, err := Scan(ctx, ScanOptions{
		HTTPClient:  srv.Client(),
		Provider:    ProviderVertexAnthropic,
		BaseURL:     srv.URL + "/v1/projects/p/locations/global",
		Candidates:  []string{"a@default", "b@default"},
		TokenSource: staticTokenSource{token: "ya29.test"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, r := range results {
		if got := r.EffectiveAccess(); got != AccessUnknown {
			t.Errorf("%s after cancel = %q, want unknown (must not be persisted as denied)", r.Name, got)
		}
	}
}

// AccessMap only exposes definitive verdicts: available (true) and denied
// (false). Unknown rows are omitted so the picker leaves them enabled, and
// legacy rows written before the Access field degrade to the corrected
// semantics rather than the old "any non-OK means denied" conflation.
func TestAccessMap_OmitsUnknownAndDerivesLegacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseURL := "https://aiplatform.googleapis.com/v1/projects/p/locations/global"
	results := []ScanResult{
		scanResult("available", AccessAvailable, "200", "accessible"),
		scanResult("denied", AccessDenied, "404", "NOT_FOUND"),
		scanResult("unknown", AccessUnknown, "ERR", "connection refused"),
		// Legacy rows (no Access field): a definitive status still
		// resolves; a transient one degrades to unknown, not denied.
		{Name: "legacy-denied", OK: false, Status: "403"},
		{Name: "legacy-transient", OK: false, Status: "ERR"},
	}
	if _, _, err := PersistScan(ProviderVertexAnthropic, baseURL, nil, results); err != nil {
		t.Fatalf("PersistScan: %v", err)
	}
	access, ok := AccessMap(ProviderVertexAnthropic, baseURL)
	if !ok {
		t.Fatal("AccessMap did not find persisted scan")
	}
	if !access["available"] {
		t.Error("available model not marked accessible")
	}
	if v, seen := access["denied"]; !seen || v {
		t.Error("denied model should be present and false")
	}
	if _, seen := access["unknown"]; seen {
		t.Error("unknown model must be omitted so the picker leaves it enabled")
	}
	if v, seen := access["legacy-denied"]; !seen || v {
		t.Error("legacy 403 row should resolve to denied")
	}
	if _, seen := access["legacy-transient"]; seen {
		t.Error("legacy transient (ERR) row must degrade to unknown, not denied")
	}
}

func TestPersistScanAndAccessMap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseURL := "https://aiplatform.googleapis.com/v1/projects/p/locations/global"
	results := []ScanResult{
		{Name: "ok-model", OK: true, Status: "200"},
		{Name: "blocked-model", OK: false, Status: "404"},
	}
	mf, path, err := PersistScan(ProviderVertexAnthropic, baseURL, []string{"ok-model", "blocked-model"}, results)
	if err != nil {
		t.Fatalf("PersistScan: %v", err)
	}
	if path == "" || len(mf.Models) != 1 || mf.Models[0] != "ok-model" {
		t.Fatalf("persisted file = %+v path=%q, want one ok model", mf, path)
	}
	access, ok := AccessMap(ProviderVertexAnthropic, baseURL)
	if !ok {
		t.Fatal("AccessMap did not find persisted scan")
	}
	if !access["ok-model"] {
		t.Error("ok-model not marked accessible")
	}
	if access["blocked-model"] {
		t.Error("blocked-model marked accessible")
	}
}
