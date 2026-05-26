package contextwindow

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestEstimateTokens_EmptyHistory(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Errorf("empty history should be 0 tokens, got %d", got)
	}
}

func TestEstimateTokens_RoughlyFourCharsPerToken(t *testing.T) {
	body := strings.Repeat("a", 1000) // 1000 chars → ~250 tokens
	got := EstimateTokens([]adapter.Message{{Role: adapter.RoleUser, Content: body}})
	if got < 240 || got > 260 {
		t.Errorf("EstimateTokens(1000 chars) = %d, want ~250", got)
	}
}

func TestEstimateTokens_CountsToolCalls(t *testing.T) {
	msg := adapter.Message{
		Role: adapter.RoleAssistant,
		ToolCalls: []adapter.ToolCall{
			{Name: "read_file", ArgsJSON: `{"path":"x"}`},
		},
	}
	got := EstimateTokens([]adapter.Message{msg})
	if got == 0 {
		t.Errorf("tool call args should contribute to token count")
	}
}

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
