package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowFor_KnownPrefixes(t *testing.T) {
	// override windowStorePathFn to avoid loading the user's overlay
	old := windowStorePathFn
	windowStorePathFn = func() (string, error) { return "", fmt.Errorf("override for test") }
	defer func() { windowStorePathFn = old }()

	cases := []struct {
		model string
		want  int
	}{
		{"gpt-4o", 128_000},
		{"gpt-4o-mini", 128_000},
		{"gpt-5", 400_000},
		{"o1-pro", 200_000},
		{"claude-sonnet-4-5", 200_000},
		{"claude-opus-4-7", 1_000_000},
		{"qwen3.5:latest", 128_000},
		{"nvidia/nemotron-3-super-120b-a12b", 262_144},
		{"nvidia/nemotron-4-340b-instruct", 262_144},
		{"nvidia/llama-3.1-nemotron-70b-instruct", 128_000},
		{"nvidia/mistral-some-other", 128_000},
	}
	for _, c := range cases {
		if got := WindowFor(c.model, 99); got != c.want {
			t.Errorf("WindowFor(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

func TestWindowFor_UnknownReturnsDefault(t *testing.T) {
	if got := WindowFor("totally-made-up:7b", 64_000); got != 64_000 {
		t.Errorf("unknown model should return default 64000, got %d", got)
	}
}

func TestWindowFor_EmptyModelReturnsDefault(t *testing.T) {
	if got := WindowFor("", 32_000); got != 32_000 {
		t.Errorf("empty model should return default, got %d", got)
	}
}

func TestEffectiveWindow_OverrideWins(t *testing.T) {
	// A positive override beats both the model-tag table and the default.
	if got := EffectiveWindow("claude-sonnet-4-5", 64_000, 999); got != 64_000 {
		t.Errorf("override should win over table 200000, got %d", got)
	}
	if got := EffectiveWindow("totally-made-up:7b", 128_000, 8_000); got != 128_000 {
		t.Errorf("override should win over default, got %d", got)
	}
}

func TestEffectiveWindow_NoOverrideFallsThrough(t *testing.T) {
	// override <= 0 means "not set": fall through to WindowFor.
	if got := EffectiveWindow("gpt-4o", 0, 999); got != 128_000 {
		t.Errorf("no override should use table 128000, got %d", got)
	}
	if got := EffectiveWindow("unknown-model", 0, 50_000); got != 50_000 {
		t.Errorf("no override + unknown model should use default 50000, got %d", got)
	}
	if got := EffectiveWindow("gpt-4o", -5, 999); got != 128_000 {
		t.Errorf("negative override should be treated as unset, got %d", got)
	}
}

// TestResolveWindowForProvider_SeparatesNamesakeModels is the
// regression test for the 2026-06-10 openai-auth context overflow:
// gpt-5.5 through the ChatGPT Codex backend must resolve to the
// measured per-backend window, never the namesake's 1.05M
// api.openai.com catalog number.
func TestResolveWindowForProvider_SeparatesNamesakeModels(t *testing.T) {
	useTempOverlay(t)

	// The provider-qualified baseline entry (openai-auth/gpt-5)
	// outranks the catalog…
	if got := ResolveWindowForProvider("openai-auth", "gpt-5.5", 0, 999); got != 264_000 {
		t.Errorf("openai-auth/gpt-5.5 = %d, want measured 264000", got)
	}
	// …and covers the whole scanned gpt-5 family behind that backend.
	if got := ResolveWindowForProvider("openai-auth", "gpt-5.4-mini", 0, 999); got != 264_000 {
		t.Errorf("openai-auth/gpt-5.4-mini = %d, want 264000", got)
	}

	// The same id behind its own provider keeps its own catalog number.
	if m, ok := FindByProviderID("openai", "gpt-5.5"); ok && m.ContextWindow > 0 {
		if got := ResolveWindowForProvider("openai", "gpt-5.5", 0, 999); got != m.ContextWindow {
			t.Errorf("openai/gpt-5.5 = %d, want catalog %d", got, m.ContextWindow)
		}
	}
}

func TestResolveWindowForProvider_OverrideStillWins(t *testing.T) {
	useTempOverlay(t)
	if got := ResolveWindowForProvider("openai-auth", "gpt-5.5", 77_777, 999); got != 77_777 {
		t.Errorf("override = %d, want 77777", got)
	}
}

// TestResolveWindowForProvider_NoQualifiedFactsKeepsLegacyResolution:
// an empty kind and a kind with no provider-qualified entries (e.g. a
// proxy fronting the real API) must behave exactly like ResolveWindow.
func TestResolveWindowForProvider_NoQualifiedFactsKeepsLegacyResolution(t *testing.T) {
	useTempOverlay(t)
	for _, kind := range []string{"", "openai-compatible"} {
		for _, model := range []string{"gpt-5.5", "nvidia/nemotron-9-future", "totally-unknown-xyz-7b"} {
			want := ResolveWindow(model, 0, 4242)
			if got := ResolveWindowForProvider(kind, model, 0, 4242); got != want {
				t.Errorf("kind %q model %q = %d, want ResolveWindow's %d", kind, model, got, want)
			}
		}
	}
}

// TestResolveWindowForProvider_OverlayPinBeatsBaselineFamily: users can
// pin a per-backend window for an exact id in the runtime overlay; the
// longer qualified prefix wins over the shipped family entry, siblings
// keep the family value.
func TestResolveWindowForProvider_OverlayPinBeatsBaselineFamily(t *testing.T) {
	useTempOverlay(t)
	if changed, err := UpsertWindow("openai-auth/gpt-5.5", 250_000); err != nil || !changed {
		t.Fatalf("UpsertWindow: changed=%v err=%v", changed, err)
	}
	if got := ResolveWindowForProvider("openai-auth", "gpt-5.5", 0, 999); got != 250_000 {
		t.Errorf("pinned = %d, want 250000", got)
	}
	if got := ResolveWindowForProvider("openai-auth", "gpt-5.4", 0, 999); got != 264_000 {
		t.Errorf("sibling = %d, want family 264000", got)
	}
}

// TestResolveWindowForProvider_CopilotUsesScannedWindows: copilot's
// models are runtime-scanned and the scan captures real per-backend
// token limits — resolution must use them instead of falling through
// to a namesake's embedded-catalog number (gpt-5.5 via Copilot is
// 400K, not api.openai.com's 1.05M).
func TestResolveWindowForProvider_CopilotUsesScannedWindows(t *testing.T) {
	useTempOverlay(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	authDir := filepath.Join(home, ".yottacode", "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := `{"cached_at":"2026-06-10T00:00:00Z","models":[
		{"id":"gpt-5.5","name":"GPT-5.5","context_window":400000,"max_output":128000},
		{"id":"no-window-model","name":"NW"}
	]}`
	if err := os.WriteFile(filepath.Join(authDir, "copilot-models.json"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ResolveWindowForProvider("copilot", "gpt-5.5", 0, 999); got != 400_000 {
		t.Errorf("copilot/gpt-5.5 = %d, want scanned 400000", got)
	}
	// A scanned entry without a window falls through to the legacy
	// layers (prefix table → default) rather than resolving to zero.
	if got := ResolveWindowForProvider("copilot", "no-window-model", 0, 4242); got != 4242 {
		t.Errorf("copilot/no-window-model = %d, want default 4242", got)
	}
}

// TestResolveWindow_LayerOrder verifies the unified resolver's priority:
// override > catalog (per-model) > prefix table > default.
func TestResolveWindow_LayerOrder(t *testing.T) {
	// 1. Override wins over everything.
	if got := ResolveWindow("gpt-4o", 12_345, 999); got != 12_345 {
		t.Errorf("override should win, got %d", got)
	}

	// 2. Catalog window is consulted directly (no override): pick a real
	// cataloged model that carries a window and confirm ResolveWindow
	// returns it rather than the (tiny) default — proving the catalog is a
	// first-class source, not just the prefix table.
	var hit *Model
	for _, m := range All() {
		if m.ContextWindow > 0 {
			mm := m
			hit = &mm
			break
		}
	}
	if hit != nil {
		if got := ResolveWindow(hit.ID, 0, 7); got != hit.ContextWindow {
			t.Errorf("ResolveWindow(%q) = %d, want catalog window %d", hit.ID, got, hit.ContextWindow)
		}
	}

	// 3. Prefix table fallback for a model not in the embedded catalog
	// (an NVIDIA NIM id) but matching a known prefix.
	if got := ResolveWindow("nvidia/nemotron-9-future", 0, 999); got != 262_144 {
		t.Errorf("prefix fallback = %d, want 262144", got)
	}

	// 4. Default floor when nothing matches.
	if got := ResolveWindow("totally-unknown-xyz-7b", 0, 50_000); got != 50_000 {
		t.Errorf("default floor = %d, want 50000", got)
	}
}
