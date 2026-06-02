package tui

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestMaybeProbeActiveModelWindowCmd_Gating verifies when the automatic
// session-start window probe fires and when it stays a no-op.
func TestMaybeProbeActiveModelWindowCmd_Gating(t *testing.T) {
	build := func(kind string, models []config.Model) Model {
		return Model{
			modelName: "deepseek-ai/deepseek-v4-flash",
			baseURL:   "https://example/v1",
			fileCfg: config.Config{
				Active:    config.Active{Provider: "p"},
				Providers: []config.Provider{{Name: "p", Kind: kind, Models: models}},
			},
		}
	}

	// openai-compatible + no override → probe.
	if build("openai-compatible", nil).maybeProbeActiveModelWindowCmd() == nil {
		t.Error("expected a probe cmd for a windowless openai-compatible model")
	}
	// window already pinned → no probe (at-most-once).
	pinned := build("openai-compatible", []config.Model{
		{Name: "deepseek-ai/deepseek-v4-flash", ContextWindow: 262144},
	})
	if pinned.maybeProbeActiveModelWindowCmd() != nil {
		t.Error("should not probe when the window is already known")
	}
	// Non-discoverable kinds → no probe. These reach the picker (and the
	// commitModelChoice gate routes through shouldProbeWindow), but each
	// carries windows in the curated catalog or speaks an API live
	// discovery can't address. Without the kind gate the picker would fire a
	// 300K-token /chat/completions probe at the ChatGPT/Copilot backends.
	for _, kind := range []string{"anthropic", "openai", "gemini", "openai-auth", "copilot"} {
		if build(kind, nil).maybeProbeActiveModelWindowCmd() != nil {
			t.Errorf("should not probe non-discoverable provider kind %q", kind)
		}
		if build(kind, nil).shouldProbeWindow(kind, "some-model") {
			t.Errorf("shouldProbeWindow(%q) = true, want false (not discoverable)", kind)
		}
	}

	// Ollama IS discoverable — via /api/show (catalog.DiscoverContextWindow),
	// not the overflow probe. A windowless Ollama model should kick off
	// discovery on startup.
	if build("ollama", nil).maybeProbeActiveModelWindowCmd() == nil {
		t.Error("expected a discovery cmd for a windowless ollama model")
	}
	if !build("ollama", nil).shouldProbeWindow("ollama", "llama3.1") {
		t.Error("shouldProbeWindow(ollama) = false, want true (resolvable via /api/show)")
	}
	// ...but an ollama model with a pinned window is not re-discovered.
	pinnedOllama := build("ollama", []config.Model{
		{Name: "deepseek-ai/deepseek-v4-flash", ContextWindow: 8192},
	})
	if pinnedOllama.maybeProbeActiveModelWindowCmd() != nil {
		t.Error("should not discover an ollama model whose window is already pinned")
	}
	// already attempted this session → no re-probe (the failure guard).
	attempted := build("openai-compatible", nil)
	attempted.probedModels = map[string]bool{"deepseek-ai/deepseek-v4-flash": true}
	if attempted.maybeProbeActiveModelWindowCmd() != nil {
		t.Error("should not re-probe a model already attempted this session")
	}
	// missing model / base URL → no probe.
	m := build("openai-compatible", nil)
	m.modelName = ""
	if m.maybeProbeActiveModelWindowCmd() != nil {
		t.Error("should not probe with an empty model name")
	}
	m2 := build("openai-compatible", nil)
	m2.baseURL = ""
	if m2.maybeProbeActiveModelWindowCmd() != nil {
		t.Error("should not probe with an empty base URL")
	}
}
