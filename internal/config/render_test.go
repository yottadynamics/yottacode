package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestRender_RoundTripsRouterRouting guards that cache-safe task routing
// (mode/fast_model/smart_model) survives a Render→decode round trip.
// Render previously emitted the [router] table only when the
// multi-provider fallback router was enabled, silently dropping these
// fields on every save — so a user's /router choice evaporated on the
// next restart. Regression for the release audit's
// render-drops-router-mode-fields finding.
func TestRender_RoundTripsRouterRouting(t *testing.T) {
	cfg := Default()
	cfg.Router.Mode = RouterModeAuto
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"

	out := Render(cfg)

	var got Config
	if _, err := toml.Decode(out, &got); err != nil {
		t.Fatalf("decode rendered config: %v\n---\n%s", err, out)
	}
	if got.Router.Mode != RouterModeAuto {
		t.Errorf("Router.Mode = %q, want %q (dropped by Render)\nrendered:\n%s", got.Router.Mode, RouterModeAuto, out)
	}
	if got.Router.FastModel != "anthropic:claude-haiku-4-5" {
		t.Errorf("Router.FastModel = %q, want anthropic:claude-haiku-4-5 (dropped by Render)", got.Router.FastModel)
	}
	if got.Router.SmartModel != "anthropic:claude-opus-4-6" {
		t.Errorf("Router.SmartModel = %q, want anthropic:claude-opus-4-6 (dropped by Render)", got.Router.SmartModel)
	}
}

// TestRender_RouterRoutingWithoutMultiProvider verifies the [router]
// table is emitted even when the multi-provider fallback router is off
// (Enabled=false, no candidates): cache-safe routing is an orthogonal
// feature that must persist on its own.
func TestRender_RouterRoutingWithoutMultiProvider(t *testing.T) {
	cfg := Default()
	cfg.Router.Enabled = false
	cfg.Router.Candidates = nil
	cfg.Router.Mode = RouterModeManual
	cfg.Router.FastModel = "openai:gpt-5-mini"
	cfg.Router.SmartModel = "openai:gpt-5"

	out := Render(cfg)
	if !strings.Contains(out, "[router]") {
		t.Fatalf("[router] table missing for routing-only config:\n%s", out)
	}
	var got Config
	if _, err := toml.Decode(out, &got); err != nil {
		t.Fatalf("decode rendered config: %v\n---\n%s", err, out)
	}
	if !got.Router.RoutingEnabled() {
		t.Errorf("routing not enabled after round trip; mode=%q", got.Router.Mode)
	}
}
