package main

import "testing"

// TestOpenAIChatModelRegex pins the chat-vs-everything-else split for
// OpenAI's list endpoint. When OpenAI ships a new family (gpt-6, o5,
// …) update the regex and add rows here. False negatives are the
// failure mode: a chat model wrongly excluded. The wizard's free-text
// input is the safety net, but we'd rather catch it in CI.
func TestOpenAIChatModelRegex(t *testing.T) {
	cases := []struct {
		id   string
		keep bool
	}{
		// Keep — all current chat families.
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"gpt-4-turbo", true},
		{"gpt-3.5-turbo", true},
		{"o1", true},
		{"o1-preview", true},
		{"o1-mini", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4", true},
		{"o4-mini", true},
		{"chatgpt-4o-latest", true},

		// Drop — non-chat surfaces and platform models.
		{"text-embedding-3-small", false},
		{"text-embedding-3-large", false},
		{"text-embedding-ada-002", false},
		{"dall-e-3", false},
		{"dall-e-2", false},
		{"tts-1", false},
		{"tts-1-hd", false},
		{"whisper-1", false},
		{"omni-moderation-latest", false},
		{"text-moderation-007", false},
		{"babbage-002", false},
		{"davinci-002", false},
		{"ft:gpt-4o:org::abc123", false}, // fine-tunes prefixed with ft:
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := openAIChatModelRegex.MatchString(tc.id)
			if got != tc.keep {
				t.Errorf("regex.Match(%q) = %v, want %v", tc.id, got, tc.keep)
			}
		})
	}
}

// TestSupportsGenerate covers the Gemini filter we apply on the
// supportedGenerationMethods array. Models without "generateContent"
// (embedding-only, countTokens-only) are dropped.
func TestSupportsGenerate(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		want    bool
	}{
		{"chat model", []string{"generateContent", "countTokens"}, true},
		{"embedding only", []string{"embedContent"}, false},
		{"empty", nil, false},
		{"with extras", []string{"countTokens", "generateContent", "embedContent"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := supportsGenerate(tc.methods); got != tc.want {
				t.Errorf("supportsGenerate(%v) = %v, want %v", tc.methods, got, tc.want)
			}
		})
	}
}
