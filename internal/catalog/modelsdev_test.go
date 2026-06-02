package catalog

import (
	"path/filepath"
	"testing"
	"time"
)

// seedModelsDev installs an in-memory models.dev catalog and points the disk
// cache at a temp dir, so tests are hermetic (no network, no real cache
// file). Pass nil to disable models.dev entirely (empty catalog). Restores
// the prior state on cleanup.
func seedModelsDev(t *testing.T, cat modelsDevCatalog) {
	t.Helper()
	if cat == nil {
		cat = modelsDevCatalog{}
	}
	dir := t.TempDir()
	modelsDevMu.Lock()
	oc, oa, ot, op := modelsDevCache, modelsDevCacheAt, modelsDevTriedNet, modelsDevCachePathFn
	modelsDevCache = cat
	modelsDevCacheAt = time.Now()
	modelsDevTriedNet = true
	modelsDevCachePathFn = func() (string, error) { return filepath.Join(dir, "mdev.json"), nil }
	modelsDevMu.Unlock()
	t.Cleanup(func() {
		modelsDevMu.Lock()
		modelsDevCache, modelsDevCacheAt, modelsDevTriedNet, modelsDevCachePathFn = oc, oa, ot, op
		modelsDevMu.Unlock()
	})
}

func TestModelsDevWindow(t *testing.T) {
	seedModelsDev(t, modelsDevCatalog{
		"nvidia": {
			ID:  "nvidia",
			API: "https://integrate.api.nvidia.com/v1",
			Models: map[string]modelsDevModel{
				"deepseek-ai/deepseek-v4-pro":                   {Limit: modelsDevLimit{Context: 1_048_576}},
				"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning": {Limit: modelsDevLimit{Context: 256_000}},
				"DeepSeek-Ai/DeepSeek-V4-Flash":                 {Limit: modelsDevLimit{Context: 1_048_576}},
			},
		},
		// A different host listing the SAME model id at a different window —
		// must NOT be returned for an nvidia base URL.
		"deepinfra": {
			ID:     "deepinfra",
			API:    "https://api.deepinfra.com/v1/openai",
			Models: map[string]modelsDevModel{"deepseek-ai/deepseek-v4-pro": {Limit: modelsDevLimit{Context: 65_536}}},
		},
	})

	// Host-matched, exact id → NVIDIA's window (not deepinfra's 65536).
	if got := ModelsDevWindow("https://integrate.api.nvidia.com/v1", "deepseek-ai/deepseek-v4-pro"); got != 1_048_576 {
		t.Errorf("nvidia deepseek-v4-pro = %d, want 1048576", got)
	}
	if got := ModelsDevWindow("https://integrate.api.nvidia.com/v1", "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning"); got != 256_000 {
		t.Errorf("nemotron = %d, want 256000", got)
	}
	// Case-insensitive id fallback.
	if got := ModelsDevWindow("https://integrate.api.nvidia.com/v1", "deepseek-ai/deepseek-v4-flash"); got != 1_048_576 {
		t.Errorf("case-insensitive deepseek-v4-flash = %d, want 1048576", got)
	}
	// Host that isn't in the catalog → 0.
	if got := ModelsDevWindow("https://my-self-hosted.internal/v1", "deepseek-ai/deepseek-v4-pro"); got != 0 {
		t.Errorf("unknown host = %d, want 0", got)
	}
	// Known host, unknown model → 0.
	if got := ModelsDevWindow("https://integrate.api.nvidia.com/v1", "no/such-model"); got != 0 {
		t.Errorf("unknown model = %d, want 0", got)
	}
	// Empty inputs → 0.
	if got := ModelsDevWindow("", "x"); got != 0 {
		t.Errorf("empty base URL = %d, want 0", got)
	}
}

func TestModelsDevWindowByProvider(t *testing.T) {
	seedModelsDev(t, modelsDevCatalog{
		// Curated lab: no `api` URL, so it can't be host-matched — only
		// name-matched. Real models.dev openai entry is shaped this way.
		"openai": {
			ID:  "openai",
			API: "",
			Models: map[string]modelsDevModel{
				"gpt-5.4":      {Limit: modelsDevLimit{Context: 1_050_000}},
				"gpt-5.4-mini": {Limit: modelsDevLimit{Context: 400_000}},
			},
		},
	})
	if got := ModelsDevWindowByProvider("openai", "gpt-5.4"); got != 1_050_000 {
		t.Errorf("by-provider gpt-5.4 = %d, want 1050000", got)
	}
	if got := ModelsDevWindowByProvider("openai", "gpt-5.4-mini"); got != 400_000 {
		t.Errorf("by-provider gpt-5.4-mini = %d, want 400000", got)
	}
	if got := ModelsDevWindowByProvider("openai", "nope"); got != 0 {
		t.Errorf("unknown model = %d, want 0", got)
	}
	if got := ModelsDevWindowByProvider("missing-provider", "gpt-5.4"); got != 0 {
		t.Errorf("unknown provider = %d, want 0", got)
	}
	// Host-matching CANNOT reach a curated lab with api="" — proving why
	// the by-provider (name) path is needed for the refresh backfill.
	if got := ModelsDevWindow("https://api.openai.com/v1", "gpt-5.4"); got != 0 {
		t.Errorf("host-match should miss an api=null provider, got %d", got)
	}
}

// TestModelsDev_EmbeddedFloor proves the committed snapshot resolves
// windows with NO in-memory cache, NO disk cache, and NO network — the
// offline-first guarantee.
func TestModelsDev_EmbeddedFloor(t *testing.T) {
	dir := t.TempDir()
	modelsDevMu.Lock()
	oc, oa, ot := modelsDevCache, modelsDevCacheAt, modelsDevTriedNet
	op, ou := modelsDevCachePathFn, modelsDevURLOverride
	modelsDevCache, modelsDevCacheAt, modelsDevTriedNet = nil, time.Time{}, false
	modelsDevCachePathFn = func() (string, error) { return filepath.Join(dir, "absent.json"), nil }
	modelsDevURLOverride = "http://127.0.0.1:0/unreachable" // dials port 0 → fails fast
	modelsDevMu.Unlock()
	t.Cleanup(func() {
		modelsDevMu.Lock()
		modelsDevCache, modelsDevCacheAt, modelsDevTriedNet = oc, oa, ot
		modelsDevCachePathFn, modelsDevURLOverride = op, ou
		modelsDevMu.Unlock()
	})

	// Any positive result here can ONLY have come from the embedded snapshot.
	if got := ModelsDevWindow("https://integrate.api.nvidia.com/v1", "deepseek-ai/deepseek-v4-pro"); got <= 0 {
		t.Errorf("offline embedded floor failed to resolve nvidia/deepseek-v4-pro (got %d)", got)
	}
	if cat := loadModelsDev(); len(cat) < 50 {
		t.Errorf("embedded snapshot has %d providers, want a full catalog (>=50)", len(cat))
	}
}

func TestHostname(t *testing.T) {
	cases := map[string]string{
		"https://integrate.api.nvidia.com/v1": "integrate.api.nvidia.com",
		"integrate.api.nvidia.com/v1":         "integrate.api.nvidia.com",
		"http://localhost:11434/v1":           "localhost",
		"":                                    "",
	}
	for in, want := range cases {
		if got := hostname(in); got != want {
			t.Errorf("hostname(%q) = %q, want %q", in, got, want)
		}
	}
}
