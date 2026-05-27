package adapter

import (
	"strings"
	"testing"
)

func TestBuildProfile_FlagsUnsupportedBuiltinToolRequests(t *testing.T) {
	profile := buildProfile(Config{
		BaseURL:              "http://localhost:11434/v1",
		Model:                "llama3.1",
		EnableWebSearch:      true,
		EnableXSearch:        true,
		ReasoningEffort:      "high",
		XSearchFromDate:      "2025-10-01",
		XSearchToDate:        "2025-10-10",
		SearchAllowedDomains: []string{"example.com"},
	}, false)

	if len(profile.Issues) == 0 {
		t.Fatalf("expected provider issues for unsupported built-ins")
	}
	got := strings.Join(profile.Issues, "\n")
	for _, want := range []string{
		"web_search requires OpenAI, xAI, or Anthropic provider-native support",
		"x_search is only available for the xAI provider",
		"x_search filters require the xAI provider",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("issues missing %q\n%s", want, got)
		}
	}
}

func TestBuildProfile_WarnsOnRemoteMissingAPIKeyAndModelMismatch(t *testing.T) {
	profile := buildProfile(Config{
		BaseURL:          "https://api.openai.com/v1",
		Model:            "grok-4",
		ProviderOverride: ProviderOpenAI,
	}, false)

	got := strings.Join(profile.Warnings, "\n")
	for _, want := range []string{
		"API key is empty for a remote provider",
		"model name looks like xAI/Grok, but provider is openai",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("warnings missing %q\n%s", want, got)
		}
	}
}

func TestBuildProfile_DefaultsWebSearchOnForOpenAIAndXAI(t *testing.T) {
	for _, cfg := range []Config{
		{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1"},
		{BaseURL: "https://api.x.ai/v1", Model: "grok-4"},
	} {
		profile := buildProfile(cfg, usesResponsesAPI(cfg))
		if !hasBuiltinTool(profile.EnabledBuiltinTools, BuiltinToolWebSearch) {
			t.Fatalf("expected default web_search for %+v: %+v", cfg, profile.EnabledBuiltinTools)
		}
	}
}

func TestBuildProfile_DisableWebSearchOptOut(t *testing.T) {
	profile := buildProfile(Config{
		BaseURL:          "https://api.openai.com/v1",
		Model:            "gpt-4.1",
		DisableWebSearch: true,
	}, usesResponsesAPI(Config{
		BaseURL:          "https://api.openai.com/v1",
		Model:            "gpt-4.1",
		DisableWebSearch: true,
	}))
	if hasBuiltinTool(profile.EnabledBuiltinTools, BuiltinToolWebSearch) {
		t.Fatalf("web_search should be disabled: %+v", profile.EnabledBuiltinTools)
	}
}

func TestDetectProviderURLPatterns(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    Provider
	}{
		{"https://chatgpt.com/backend-api/codex", ProviderOpenAIAuth},
		{"https://chatgpt.com/backend-api/codex/responses", ProviderOpenAIAuth},
		{"https://api.githubcopilot.com", ProviderCopilot},
		{"https://api.githubcopilot.com/chat/completions", ProviderCopilot},
		{"https://api.openai.com/v1", ProviderOpenAI},
		{"https://api.anthropic.com", ProviderAnthropic},
		{"https://api.x.ai/v1", ProviderXAI},
		{"https://generativelanguage.googleapis.com", ProviderGemini},
		{"http://localhost:11434/v1", ProviderOllama},
		{"https://my-ollama-proxy.example.com", ProviderOllama},
		{"https://random.example.com/v1", ProviderOpenAICompatible},
	} {
		got := detectProvider(tc.baseURL, "")
		if got != tc.want {
			t.Errorf("detectProvider(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}

func TestDetectProviderOverrideWins(t *testing.T) {
	got := detectProvider("https://api.openai.com/v1", ProviderOpenAIAuth)
	if got != ProviderOpenAIAuth {
		t.Errorf("override should win, got %q", got)
	}
}

func TestBuildProfile_SupportsImages(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		want     bool
	}{
		{ProviderAnthropic, true},
		{ProviderOpenAI, true},
		{ProviderOpenAIAuth, true},
		{ProviderCopilot, true},
		{ProviderGemini, true},
		{ProviderXAI, true},
		{ProviderOllama, false},
		{ProviderOpenAICompatible, false},
	} {
		profile := buildProfile(Config{ProviderOverride: tc.provider, Model: "test"}, false)
		if profile.SupportsImages != tc.want {
			t.Errorf("SupportsImages for %q = %v, want %v", tc.provider, profile.SupportsImages, tc.want)
		}
	}
}
