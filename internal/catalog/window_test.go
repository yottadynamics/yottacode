package catalog

import "testing"

func TestWindowFor_KnownPrefixes(t *testing.T) {
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
