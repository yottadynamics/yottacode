package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempOverlay points the runtime overlay at a temp file and resets the
// store cache, restoring both on cleanup so other tests in the package see
// the real (embedded-only) state again.
func useTempOverlay(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "context-windows.json")
	orig := windowStorePathFn

	windowStoreMu.Lock()
	windowStorePathFn = func() (string, error) { return path, nil }
	windowStoreLoaded = false
	windowOverlayCache, windowBaselineCache = nil, nil
	windowStoreMu.Unlock()

	t.Cleanup(func() {
		windowStoreMu.Lock()
		windowStorePathFn = orig
		windowStoreLoaded = false
		windowOverlayCache, windowBaselineCache = nil, nil
		windowStoreMu.Unlock()
	})
	return path
}

func TestWindowStore_EmbeddedBaselineLoads(t *testing.T) {
	useTempOverlay(t) // overlay points at a non-existent temp file → embedded only
	// Values from the committed/embedded context-windows.json.
	if got := WindowFor("nvidia/nemotron-3-super", 0); got != 262_144 {
		t.Errorf("embedded baseline nvidia/nemotron = %d, want 262144", got)
	}
	if got := WindowFor("gpt-4o-mini", 0); got != 128_000 {
		t.Errorf("embedded baseline gpt-4o = %d, want 128000", got)
	}
	if got := WindowFor("totally-unknown-7b", 4242); got != 4242 {
		t.Errorf("unknown model should fall to default, got %d", got)
	}
}

func TestWindowStore_OverlayOverridesBaseline(t *testing.T) {
	path := useTempOverlay(t)
	// Overlay overrides a baseline family prefix and adds a new model.
	overlay := `{"entries":[
		{"prefix":"gpt-4o","window":999999},
		{"prefix":"my-custom/model","window":48000}
	]}`
	if err := os.WriteFile(path, []byte(overlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	// Force a reload now that the file exists.
	windowStoreMu.Lock()
	windowStoreLoaded = false
	windowStoreMu.Unlock()

	if got := WindowFor("gpt-4o-mini", 0); got != 999999 {
		t.Errorf("overlay should override baseline gpt-4o, got %d", got)
	}
	if got := WindowFor("my-custom/model-x", 0); got != 48000 {
		t.Errorf("overlay-only entry = %d, want 48000", got)
	}
	// A model the overlay doesn't mention still resolves from the baseline.
	if got := WindowFor("claude-sonnet-4-5", 0); got != 200_000 {
		t.Errorf("baseline still used for non-overlaid model, got %d", got)
	}
}

func TestUpsertWindow_PersistsAndUpdates(t *testing.T) {
	useTempOverlay(t)
	if err := UpsertWindow("deepseek-ai/deepseek-v4-pro", 262_144); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := WindowFor("deepseek-ai/deepseek-v4-pro", 0); got != 262_144 {
		t.Errorf("after upsert = %d, want 262144 (overlay should beat the deepseek- baseline 64000)", got)
	}
	// Re-upsert updates in place (no duplicate).
	if err := UpsertWindow("deepseek-ai/deepseek-v4-pro", 131_072); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := WindowFor("deepseek-ai/deepseek-v4-pro", 0); got != 131_072 {
		t.Errorf("after re-upsert = %d, want 131072", got)
	}
	// No-ops.
	if err := UpsertWindow("", 1000); err != nil {
		t.Errorf("empty model should be a no-op, got %v", err)
	}
	if err := UpsertWindow("x", 0); err != nil {
		t.Errorf("non-positive window should be a no-op, got %v", err)
	}
}
