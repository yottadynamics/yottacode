// Package contextwindow estimates token usage for a message history.
// The numbers are approximate — exact tokenization depends on the model's
// tokenizer and varies enough between providers that "good enough for a
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
//
// The companion question — how big IS the model's window — lives in the
// catalog package (catalog.ResolveWindow / WindowFor): the window is a
// per-model/per-deployment fact sourced from the catalog, the user's
// override, or a live probe, none of which belong in this leaf estimation
// package (which would otherwise drag the catalog's config/network deps in,
// and form an import cycle via adapter).
package contextwindow

import (
	"encoding/json"

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
