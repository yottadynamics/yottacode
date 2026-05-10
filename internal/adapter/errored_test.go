package adapter

import (
	"context"
	"strings"
	"testing"
)

// TestNewWithConfig_EmptyKeyAtCloudHostFailsFast is the regression test
// for the silent-401 bug: configuring openai/anthropic/gemini/xai with
// no API key used to dispatch to the real adapter, which substituted
// "local-no-auth" to satisfy the SDK's non-empty-string requirement.
// The upstream then returned 401 with a confusing message about that
// sentinel. We now intercept at construction time and emit a clean
// "no API key configured" error event on first ChatStream.
func TestNewWithConfig_EmptyKeyAtCloudHostFailsFast(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		model    string
		wantHint string
	}{
		{"openai", "https://api.openai.com/v1", "gpt-4o", "OPENAI_API_KEY"},
		{"anthropic", "https://api.anthropic.com", "claude-sonnet-4-6", "ANTHROPIC_API_KEY"},
		{"gemini", "https://generativelanguage.googleapis.com", "gemini-2.5-flash", "GEMINI_API_KEY"},
		{"xai", "https://api.x.ai/v1", "grok-3", "XAI_API_KEY"},
		{"openai-compatible-remote", "https://integrate.api.nvidia.com/v1", "nvidia/x", "api_key_env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewWithConfig(Config{BaseURL: tc.baseURL, Model: tc.model, APIKey: ""})
			if _, ok := c.(*erroredAdapter); !ok {
				t.Fatalf("expected erroredAdapter for empty key on %s; got %T", tc.name, c)
			}
			ch := c.ChatStream(context.Background(), nil, nil)
			var sawErr error
			for ev := range ch {
				if ev.Err != nil {
					sawErr = ev.Err
				}
			}
			if sawErr == nil {
				t.Fatalf("expected an error event from erroredAdapter on %s", tc.name)
			}
			if !strings.Contains(sawErr.Error(), tc.wantHint) {
				t.Errorf("error %q should mention %q so the user knows what to set",
					sawErr.Error(), tc.wantHint)
			}
		})
	}
}

// TestNewWithConfig_LoopbackIsExempt covers the self-hosted /
// httptest case: localhost / 127.0.0.1 / [::1] base URLs don't need
// an API key, since they cover vLLM, Llama Stack, Ollama on a
// non-default port, and our own adapter test fixtures.
func TestNewWithConfig_LoopbackIsExempt(t *testing.T) {
	for _, url := range []string{
		"http://localhost:8080/v1",
		"http://127.0.0.1:9999",
		"https://[::1]:5555/v1",
	} {
		t.Run(url, func(t *testing.T) {
			c := NewWithConfig(Config{BaseURL: url, Model: "anything", APIKey: ""})
			if _, ok := c.(*erroredAdapter); ok {
				t.Errorf("loopback URL %q should not require an API key", url)
			}
		})
	}
}

// TestNewWithConfig_OllamaIsExempt: Ollama doesn't validate keys, so
// an empty key is the normal case. Substitution to "local-no-auth"
// happens inside the chat adapter (the SDK requires non-empty); the
// router shouldn't refuse to construct.
func TestNewWithConfig_OllamaIsExempt(t *testing.T) {
	c := NewWithConfig(Config{BaseURL: "http://ollama.internal:11434/v1", Model: "qwen3.5:9b"})
	if _, ok := c.(*erroredAdapter); ok {
		t.Errorf("Ollama should be exempt from the empty-key gate")
	}
}
