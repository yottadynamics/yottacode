package mcp

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// extractText is unexported, so this test lives in package mcp (not
// mcp_test) so it can call the function directly with hand-built
// CallToolResult instances. That lets us assert behavior on every
// SDK content type without needing a server fixture that emits each
// variant.

func TestExtractText_TextContentPassesThrough(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: "hello"},
			&sdk.TextContent{Text: "world"},
		},
	}
	got := extractText(res)
	if got != "hello\nworld" {
		t.Errorf("text passthrough = %q, want %q", got, "hello\nworld")
	}
}

func TestExtractText_NilResultReturnsEmpty(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("extractText(nil) = %q, want empty", got)
	}
}

func TestExtractText_ImageContentBecomesMarker(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3, 4, 5}},
		},
	}
	got := extractText(res)
	if !strings.Contains(got, "image omitted") {
		t.Errorf("image content should produce an [image omitted ...] marker; got %q", got)
	}
	if !strings.Contains(got, "image/png") {
		t.Errorf("marker should include the MIME type; got %q", got)
	}
	if !strings.Contains(got, "5 bytes") {
		t.Errorf("marker should include the payload size; got %q", got)
	}
}

func TestExtractText_AudioContentBecomesMarker(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.AudioContent{MIMEType: "audio/wav", Data: []byte{0, 0, 0}},
		},
	}
	got := extractText(res)
	if !strings.Contains(got, "audio omitted") {
		t.Errorf("audio content should produce an [audio omitted ...] marker; got %q", got)
	}
	if !strings.Contains(got, "audio/wav") {
		t.Errorf("marker should include the MIME type; got %q", got)
	}
}

func TestExtractText_ResourceLinkBecomesMarker(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.ResourceLink{
				URI:      "file:///workspace/notes.md",
				MIMEType: "text/markdown",
			},
		},
	}
	got := extractText(res)
	if !strings.Contains(got, "resource link omitted") {
		t.Errorf("resource link should produce a marker; got %q", got)
	}
	if !strings.Contains(got, "file:///workspace/notes.md") {
		t.Errorf("marker should include the URI; got %q", got)
	}
}

func TestExtractText_EmbeddedResourceBecomesMarker(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.EmbeddedResource{
				Resource: &sdk.ResourceContents{URI: "mcp://memory/recent"},
			},
		},
	}
	got := extractText(res)
	if !strings.Contains(got, "embedded resource omitted") {
		t.Errorf("embedded resource should produce a marker; got %q", got)
	}
	if !strings.Contains(got, "mcp://memory/recent") {
		t.Errorf("marker should include the URI; got %q", got)
	}
}

func TestExtractText_MixedContentJoinsBlocks(t *testing.T) {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: "preamble"},
			&sdk.ImageContent{MIMEType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}},
			&sdk.TextContent{Text: "trailer"},
		},
	}
	got := extractText(res)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("mixed content = %d lines, want 3; got %q", len(lines), got)
	}
	if lines[0] != "preamble" || lines[2] != "trailer" {
		t.Errorf("text blocks should sit at their original positions; got %q", got)
	}
	if !strings.Contains(lines[1], "image omitted") {
		t.Errorf("middle block should be the image marker; got %q", lines[1])
	}
}

func TestExtractText_StructuredContentFallback(t *testing.T) {
	// A server populated StructuredContent directly and left Content
	// empty. The bridge should marshal it as JSON so the model still
	// sees the result.
	res := &sdk.CallToolResult{
		StructuredContent: map[string]any{"count": 42, "status": "ok"},
	}
	got := extractText(res)
	if !strings.Contains(got, `"count":42`) || !strings.Contains(got, `"status":"ok"`) {
		t.Errorf("StructuredContent should JSON-marshal as the fallback; got %q", got)
	}
}

func TestExtractText_StructuredContentSkippedWhenTextPresent(t *testing.T) {
	// When Content has text, StructuredContent is redundant (the SDK's
	// typed-output path auto-syncs them) and we don't double-report.
	res := &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: "{\"count\":42}"}},
		StructuredContent: map[string]any{"count": 42},
	}
	got := extractText(res)
	if got != `{"count":42}` {
		t.Errorf("text + structured (redundant) should yield only the text; got %q", got)
	}
}

func TestExtractText_StructuredContentUsedWhenOnlyNonTextBlocks(t *testing.T) {
	// Image-only Content with a parallel StructuredContent should
	// surface both: the image marker AND the JSON, so the model sees
	// the structured data and learns the image was dropped.
	res := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1}},
		},
		StructuredContent: map[string]any{"width": 100, "height": 50},
	}
	got := extractText(res)
	if !strings.Contains(got, "image omitted") {
		t.Errorf("image marker should appear; got %q", got)
	}
	if !strings.Contains(got, `"width":100`) {
		t.Errorf("StructuredContent should also appear when no text block was present; got %q", got)
	}
}

func TestExtractText_NilContentBlockIsSkipped(t *testing.T) {
	// Defensive: the SDK shouldn't put nil entries in Content, but if
	// some malformed-server case ever did, we shouldn't panic.
	var nilText *sdk.TextContent
	var nilImage *sdk.ImageContent
	res := &sdk.CallToolResult{
		Content: []sdk.Content{nilText, nilImage, &sdk.TextContent{Text: "survivor"}},
	}
	got := extractText(res)
	if got != "survivor" {
		t.Errorf("nil entries should be silently skipped, leaving non-nil blocks; got %q", got)
	}
}

func TestExtractText_EmptyResultReturnsEmpty(t *testing.T) {
	// A genuinely empty result (no Content, no StructuredContent)
	// should return the empty string — distinct from "dropped content
	// produced a marker." The bridge's caller propagates this as a
	// successful empty tool_result; the model decides what to do.
	got := extractText(&sdk.CallToolResult{})
	if got != "" {
		t.Errorf("empty result should yield empty string; got %q", got)
	}
}
