package catalog

import "testing"

// ModelsDevLimitsByProvider backs the catalog refresh's backfill. MaxOutput
// is as often missing from a vendor's own API as ContextWindow — OpenAI's
// /v1/models reports neither — and a zero there silently downgrades a
// model's extended-thinking budget to a conservative default.
func TestModelsDevLimitsByProvider(t *testing.T) {
	seedModelsDev(t, modelsDevCatalog{
		"openai": {
			ID: "openai",
			Models: map[string]modelsDevModel{
				"gpt-5":      {Limit: modelsDevLimit{Context: 400000, Output: 128000}},
				"ctx-only":   {Limit: modelsDevLimit{Context: 128000}},
				"out-only":   {Limit: modelsDevLimit{Output: 4096}},
				"no-limits":  {},
				"MiXeD-CaSe": {Limit: modelsDevLimit{Context: 64000, Output: 8192}},
			},
		},
	})

	tests := []struct {
		name       string
		provider   string
		model      string
		wantCtx    int
		wantOutput int
	}{
		{name: "both limits", provider: "openai", model: "gpt-5", wantCtx: 400000, wantOutput: 128000},
		{name: "context only", provider: "openai", model: "ctx-only", wantCtx: 128000},
		{name: "output only", provider: "openai", model: "out-only", wantOutput: 4096},
		{name: "entry with no limits", provider: "openai", model: "no-limits"},
		{name: "case-insensitive fallback", provider: "openai", model: "mixed-case", wantCtx: 64000, wantOutput: 8192},
		{name: "unknown model", provider: "openai", model: "nope"},
		{name: "unknown provider", provider: "nope", model: "gpt-5"},
		{name: "empty provider", provider: "", model: "gpt-5"},
		{name: "empty model", provider: "openai", model: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, out := ModelsDevLimitsByProvider(tc.provider, tc.model)
			if ctx != tc.wantCtx {
				t.Errorf("contextWindow = %d, want %d", ctx, tc.wantCtx)
			}
			if out != tc.wantOutput {
				t.Errorf("maxOutput = %d, want %d", out, tc.wantOutput)
			}
		})
	}
}

// The window accessor is now a thin wrapper over the limits one; its
// existing contract must not shift.
func TestModelsDevWindowByProvider_StillAgreesWithLimits(t *testing.T) {
	seedModelsDev(t, modelsDevCatalog{
		"openai": {
			ID: "openai",
			Models: map[string]modelsDevModel{
				"gpt-5":     {Limit: modelsDevLimit{Context: 400000, Output: 128000}},
				"out-only":  {Limit: modelsDevLimit{Output: 4096}},
				"no-limits": {},
			},
		},
	})
	for _, model := range []string{"gpt-5", "out-only", "no-limits", "absent"} {
		want, _ := ModelsDevLimitsByProvider("openai", model)
		if got := ModelsDevWindowByProvider("openai", model); got != want {
			t.Errorf("ModelsDevWindowByProvider(%q) = %d, want %d", model, got, want)
		}
	}
}
