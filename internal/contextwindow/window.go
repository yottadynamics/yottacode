// Package contextwindow estimates token usage for a message history and
// looks up the model's context-window capacity. Both numbers are
// approximate — exact tokenization depends on the model's tokenizer
// and varies enough between providers that "good enough for a
// watermark" is the operative bar.
//
// Two callers consume this:
//
//   - The TUI status bar, which colors the token counter when usage
//     crosses a configurable warn_threshold.
//   - The auto-summarization trigger, which fires before the next turn
//     when usage crosses auto_threshold.
//
// Both treat the numbers as ratios, not absolute counts, so a 10–15%
// estimation error is harmless.
package contextwindow

import (
	"encoding/json"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// charsPerToken is the rough ~4-chars-per-token heuristic that holds for
// English text across most BPE-family tokenizers (GPT, Claude, Llama).
// Exposed as a constant so EstimateText and EstimateTokens share it.
const charsPerToken = 4

// EstimateText returns an approximate token count for an arbitrary
// string under the same heuristic EstimateTokens uses. /context calls
// this for the system-prompt and memory-file buckets where the input
// isn't a Message slice.
func EstimateText(s string) int {
	return (len(s) + charsPerToken - 1) / charsPerToken
}

// EstimateTokens returns an approximate token count for the given
// messages. Tool-call argument JSON and tool-result content are
// counted; role metadata is not (it's bounded and small).
func EstimateTokens(messages []adapter.Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Name) + len(tc.ArgsJSON)
		}
	}
	return (chars + charsPerToken - 1) / charsPerToken
}

// EstimateToolSchemas counts what the adapter advertises per tool: the
// tool name, description, and the JSON-serialized parameter schema.
// Mirrors what's actually sent on the wire so the /context Tools bucket
// matches the real cost.
//
// Marshal errors fall back to a zero contribution for the offending
// tool — a malformed schema would have failed at registration time, so
// in practice this branch only fires for tools that pass nil/non-JSON
// schemas (which the adapter would reject anyway).
func EstimateToolSchemas(tools []adapter.Tool) int {
	chars := 0
	for _, t := range tools {
		chars += len(t.Name) + len(t.Description)
		if t.Schema != nil {
			if buf, err := json.Marshal(t.Schema); err == nil {
				chars += len(buf)
			}
		}
	}
	return (chars + charsPerToken - 1) / charsPerToken
}

// SplitMessages partitions a message slice into the system-prompt
// prefix and the conversation body, returning estimated token counts
// for each bucket. The split is by role: every RoleSystem message
// (typically just one) goes into the system bucket, everything else
// into conversation. /context charges the two to different buckets so
// the user can see the always-present system overhead separately from
// the growing chat history.
func SplitMessages(msgs []adapter.Message) (systemTokens, conversationTokens int) {
	systemChars := 0
	convoChars := 0
	for _, m := range msgs {
		c := len(m.Content)
		for _, tc := range m.ToolCalls {
			c += len(tc.Name) + len(tc.ArgsJSON)
		}
		if m.Role == adapter.RoleSystem {
			systemChars += c
		} else {
			convoChars += c
		}
	}
	systemTokens = (systemChars + charsPerToken - 1) / charsPerToken
	conversationTokens = (convoChars + charsPerToken - 1) / charsPerToken
	return
}

// WindowFor returns the context-window capacity (in tokens) for the
// given model tag, falling back to defaultWindow when the model isn't
// in the lookup table. Matching is by lowercase prefix — generous on
// purpose so unknown variants of known families still resolve.
//
// The table is intentionally short. We update it as model families
// ship; users with an exotic or self-hosted model can always pin a
// fallback via context.default_window in config.toml.
func WindowFor(model string, defaultWindow int) int {
	tag := strings.ToLower(strings.TrimSpace(model))
	if tag == "" {
		return defaultWindow
	}
	for _, e := range knownWindows {
		if strings.HasPrefix(tag, e.prefix) {
			return e.window
		}
	}
	return defaultWindow
}

type windowEntry struct {
	prefix string
	window int
}

// knownWindows maps lowercase model-tag prefixes to context-window sizes
// in tokens. Order matters — longer/more-specific prefixes first so a
// "gpt-5-mini" tag matches the gpt-5 entry instead of falling through.
//
// Numbers are vendor-published as of late 2025 / early 2026. When a
// model lists "input + output" separately we use input — that's what
// gates the warn/auto thresholds.
var knownWindows = []windowEntry{
	// OpenAI reasoning + flagship
	{"o1", 200_000},
	{"o3", 200_000},
	{"o4", 200_000},
	{"gpt-5", 400_000},
	{"gpt-4.1", 1_000_000},
	{"gpt-4o", 128_000},
	{"gpt-4-turbo", 128_000},
	{"gpt-4", 8_192},
	{"gpt-3.5", 16_385},

	// Anthropic
	{"claude-opus-4", 1_000_000},
	{"claude-sonnet-4", 200_000},
	{"claude-haiku-4", 200_000},
	{"claude-3-7", 200_000},
	{"claude-3-5", 200_000},
	{"claude-3", 200_000},
	{"claude-", 200_000},

	// Google
	{"gemini-2.5", 1_000_000},
	{"gemini-2", 1_000_000},
	{"gemini-1.5", 1_000_000},
	{"gemini-", 32_768},

	// xAI
	{"grok-4", 256_000},
	{"grok-3", 131_072},
	{"grok-", 131_072},

	// Meta / Llama
	{"llama-3.3", 128_000},
	{"llama-3.2", 128_000},
	{"llama-3.1", 128_000},
	{"llama-3", 8_192},
	{"llama3.3", 128_000},
	{"llama3.2", 128_000},
	{"llama3.1", 128_000},
	{"llama3", 8_192},

	// Alibaba / Qwen
	{"qwen3", 128_000},
	{"qwen2.5", 128_000},
	{"qwen-2.5", 128_000},
	{"qwen2", 32_768},

	// DeepSeek
	{"deepseek-v3", 128_000},
	{"deepseek-r1", 128_000},
	{"deepseek-", 64_000},

	// Mistral
	{"mistral-large", 128_000},
	{"mistral-", 32_768},
	{"mixtral-", 32_768},

	// NVIDIA NIM. Nemotron-3-Super publishes a 262K window via
	// integrate.api.nvidia.com; other NVIDIA-hosted models default to
	// 128K which is the lowest-common-denominator across the catalog.
	{"nvidia/nemotron", 262_144},
	{"nvidia/", 128_000},
}
