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

func TestEstimateText_EmptyAndRough(t *testing.T) {
	if got := EstimateText(""); got != 0 {
		t.Errorf("empty string = %d, want 0", got)
	}
	if got := EstimateText(strings.Repeat("a", 1000)); got < 240 || got > 260 {
		t.Errorf("EstimateText(1000 chars) = %d, want ~250", got)
	}
}

func TestEstimateToolSchemas_CountsNameDescriptionSchema(t *testing.T) {
	tools := []adapter.Tool{
		{
			Name:        "read_file",
			Description: "reads a file from disk",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
	}
	got := EstimateToolSchemas(tools)
	if got == 0 {
		t.Fatalf("schema bytes should produce non-zero tokens")
	}
	// Tighten the lower bound: name+desc alone is ~32 chars / 8 tokens;
	// the marshaled schema adds another ~70+ chars. So a single tool
	// should land north of 20 tokens.
	if got < 20 {
		t.Errorf("EstimateToolSchemas single tool = %d, want >=20", got)
	}
}

func TestEstimateToolSchemas_NilSchemaIsCountedAsNameOnly(t *testing.T) {
	tools := []adapter.Tool{{Name: "x", Description: "y"}}
	got := EstimateToolSchemas(tools)
	if got == 0 {
		t.Errorf("name + description should contribute even with nil schema")
	}
}

func TestEstimateToolSchemas_Empty(t *testing.T) {
	if got := EstimateToolSchemas(nil); got != 0 {
		t.Errorf("empty tool slice = %d, want 0", got)
	}
}

func TestSplitMessages_SeparatesSystemFromConversation(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: strings.Repeat("s", 400)},  // ~100 tokens
		{Role: adapter.RoleUser, Content: strings.Repeat("u", 800)},    // ~200 tokens
		{Role: adapter.RoleAssistant, Content: strings.Repeat("a", 400)}, // ~100 tokens
	}
	sys, convo := SplitMessages(msgs)
	if sys < 95 || sys > 105 {
		t.Errorf("system tokens = %d, want ~100", sys)
	}
	if convo < 295 || convo > 305 {
		t.Errorf("conversation tokens = %d, want ~300", convo)
	}
}

func TestSplitMessages_NoSystem(t *testing.T) {
	sys, convo := SplitMessages([]adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
	})
	if sys != 0 {
		t.Errorf("no system message, want sys=0, got %d", sys)
	}
	if convo == 0 {
		t.Errorf("conversation should be non-zero")
	}
}

func TestSplitMessages_SumMatchesEstimateTokens(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "system instructions"},
		{Role: adapter.RoleUser, Content: "hello"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{Name: "read_file", ArgsJSON: `{"path":"main.go"}`},
		}},
		{Role: adapter.RoleTool, Content: "file content"},
	}
	sys, convo := SplitMessages(msgs)
	total := EstimateTokens(msgs)
	if sys+convo != total {
		t.Errorf("SplitMessages sum (%d) != EstimateTokens (%d)", sys+convo, total)
	}
}
