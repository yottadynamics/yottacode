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
	"bytes"
	"encoding/json"
	"image"
	// Registers the decoders image.DecodeConfig needs to read a header.
	// Config-only: these parse dimensions, they never decode pixels.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// charsPerToken is the rough ~4-chars-per-token heuristic that holds for
// English text across most BPE-family tokenizers (GPT, Claude, Llama).
// Exposed as a constant so EstimateText and EstimateTokens share it.
const charsPerToken = 4

// Image cost constants. Vision models bill an image by its pixel area, not
// its file size: Anthropic documents tokens ≈ (width × height) / 750, and
// OpenAI's tile scheme lands in the same order of magnitude. Both clamp
// large images down to a max resolution before billing, so cost saturates
// rather than growing without bound.
//
// pixelsPerImageToken is that divisor; maxImageTokens is the ceiling for a
// full-size image (Anthropic's ~1092×1092 cap ≈ 1590 tokens).
// fallbackImageTokens is used when the header won't parse — an unknown image
// must not score zero, which is the bug this whole path exists to prevent.
const (
	pixelsPerImageToken = 750
	maxImageTokens      = 1590
	fallbackImageTokens = 1000
)

// EstimateImage returns the approximate token cost of a single image.
//
// This deliberately does NOT scale with len(img.Data). A 4MB screenshot and
// a 200KB one of the same dimensions cost the model the same — byte size
// reflects compression, not context. Estimating from bytes would have
// scored one screenshot at a million tokens and triggered compaction
// constantly; ignoring images (the prior behavior) scored them at zero, so
// an image-heavy session could never trigger compaction at all. Pixel area
// is the measure both major providers actually bill on.
//
// DecodeConfig reads only the header, so this stays cheap enough for the
// per-turn and status-bar call sites.
//
// img.MediaType is honored: only formats with a registered header decoder
// can be dimension-read, and webp — which the read_file tool actively
// advertises — has none in the stdlib's image package. Rather than pull in
// a webp decoder (golang.org/x/image) just to read a width, we skip the
// sniff for any MediaType we can't decode and return the fallback. That's
// the same outcome the byte-sniff would have produced for an unregistered
// format, but explicit and stable rather than accidental.
func EstimateImage(img adapter.ImageBlock) int {
	if !decodableMediaType(img.MediaType) {
		return fallbackImageTokens
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return fallbackImageTokens
	}
	tokens := (cfg.Width*cfg.Height + pixelsPerImageToken - 1) / pixelsPerImageToken
	if tokens > maxImageTokens {
		return maxImageTokens
	}
	return tokens
}

// decodableMediaType reports whether img.MediaType names a format the
// stdlib image package knows how to header-decode. The blank import block
// above registers gif, jpeg, and png — nothing else — so any other
// MediaType short-circuits to the fallback rather than spending a sniff
// that's guaranteed to fail.
func decodableMediaType(mt string) bool {
	switch mt {
	case "image/png", "image/jpeg", "image/gif":
		return true
	}
	// Unknown / empty lets the sniff run so a correctly-sized payload with
	// a missing MediaType is still billed by its dimensions rather than
	// always paying the flat fallback.
	return mt == ""
}

// EstimateMessage returns the approximate token cost of one message —
// content, tool-call arguments, and any attached images.
//
// Callers that walk long histories (compaction's retain-point search) use
// this per-message form to avoid building a slice per call.
func EstimateMessage(m adapter.Message) int {
	chars := len(m.Content)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Name) + len(tc.ArgsJSON)
	}
	tokens := (chars + charsPerToken - 1) / charsPerToken
	for _, img := range m.Images {
		tokens += EstimateImage(img)
	}
	return tokens
}

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
	total := 0
	for _, m := range messages {
		total += EstimateMessage(m)
	}
	return total
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
	// Per-message via EstimateMessage rather than summing chars per bucket
	// and rounding once: that keeps sys+convo == EstimateTokens exactly (both
	// sum the same per-message figure), and it picks up image cost, which the
	// char-only walk here used to drop from the /context breakdown.
	for _, m := range msgs {
		if m.Role == adapter.RoleSystem {
			systemTokens += EstimateMessage(m)
		} else {
			conversationTokens += EstimateMessage(m)
		}
	}
	return
}
