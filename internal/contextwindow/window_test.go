package contextwindow

import (
	"bytes"
	"image"
	"image/png"
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
		{Role: adapter.RoleSystem, Content: strings.Repeat("s", 400)},    // ~100 tokens
		{Role: adapter.RoleUser, Content: strings.Repeat("u", 800)},      // ~200 tokens
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

// encodePNG builds a real w×h PNG so the estimator's header decode runs
// against genuine bytes rather than a hand-rolled fixture.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestEstimateImage_ScalesWithPixelsNotBytes is the core of the fix. An
// image's context cost tracks its pixel area — a heavily-compressed and a
// bloated encoding of the same dimensions cost the model the same. Sizing
// off len(Data) would have made one screenshot look like a million tokens.
func TestEstimateImage_ScalesWithPixelsNotBytes(t *testing.T) {
	small := EstimateImage(adapter.ImageBlock{Data: encodePNG(t, 100, 100), MediaType: "image/png"})
	large := EstimateImage(adapter.ImageBlock{Data: encodePNG(t, 400, 400), MediaType: "image/png"})

	// 100×100 = 10k px / 750 ≈ 14 tokens; 400×400 = 160k px / 750 ≈ 214.
	if small < 10 || small > 20 {
		t.Errorf("100x100 ≈ %d tokens, want ~14", small)
	}
	if large < 200 || large > 230 {
		t.Errorf("400x400 ≈ %d tokens, want ~214", large)
	}
	if large <= small {
		t.Errorf("larger image must cost more: %d vs %d", large, small)
	}

	// Byte size must not drive the estimate: padding the payload with
	// trailing bytes leaves the dimensions — and so the cost — unchanged.
	padded := append(encodePNG(t, 100, 100), bytes.Repeat([]byte{0}, 4<<20)...)
	if got := EstimateImage(adapter.ImageBlock{Data: padded, MediaType: "image/png"}); got != small {
		t.Errorf("4MB of padding changed the estimate: %d vs %d", got, small)
	}
}

// TestEstimateImage_CapsAndFallback: cost saturates for very large images
// (providers downscale before billing), and an unparseable payload must
// never score zero — scoring zero is exactly what let image-heavy sessions
// grow without ever tripping compaction.
func TestEstimateImage_CapsAndFallback(t *testing.T) {
	huge := EstimateImage(adapter.ImageBlock{Data: encodePNG(t, 4000, 4000), MediaType: "image/png"})
	if huge != maxImageTokens {
		t.Errorf("oversized image = %d tokens, want the %d cap", huge, maxImageTokens)
	}
	junk := EstimateImage(adapter.ImageBlock{Data: []byte("not an image"), MediaType: "image/png"})
	if junk != fallbackImageTokens {
		t.Errorf("undecodable image = %d, want fallback %d", junk, fallbackImageTokens)
	}
	if EstimateImage(adapter.ImageBlock{}) == 0 {
		t.Error("empty image scored 0 tokens; must never be free")
	}
}

// TestEstimateImage_HonorsMediaType gates the fix: the read_file tool and
// TUI paste path set MediaType="image/webp" for .webp files, and the stdlib
// image package has no webp decoder registered. Without honoring MediaType
// the function would sniff, fail the sniff, and return fallbackImageTokens
// — which happened to be right only by accident. With honoring, any
// MediaType outside the registered set (png/jpeg/gif) short-circuits to the
// same fallback, explicitly and stably rather than as a sniff accident.
// A registered MediaType still gets dimensioned billing; a blank MediaType
// still gets a sniff so a payload built without the hint is billed by its
// pixels when decodable.
func TestEstimateImage_HonorsMediaType(t *testing.T) {
	pngBytes := encodePNG(t, 800, 600)
	// Registered MediaType → dimension-based cost (~640 tokens), not the
	// flat 1000 fallback.
	got := EstimateImage(adapter.ImageBlock{Data: pngBytes, MediaType: "image/png"})
	if got == fallbackImageTokens {
		t.Errorf("png with registered MediaType should be dimension-billed, got fallback %d", got)
	}
	if got < 600 || got > 700 {
		t.Errorf("800x600 png = %d tokens, want ~640", got)
	}

	// Same bytes, but a MediaType we can't header-decode (webp) must pay the
	// flat fallback — regardless of what the payload actually is. This is
	// the case that used to sniff-fail silently.
	webp := EstimateImage(adapter.ImageBlock{Data: pngBytes, MediaType: "image/webp"})
	if webp != fallbackImageTokens {
		t.Errorf("webp MediaType must fall back to %d, got %d", fallbackImageTokens, webp)
	}
	// Animated-heic / avif aren't produced anywhere today, but the rule is
	// the same: unknown MediaType → fallback, never zero, never a guess.
	heic := EstimateImage(adapter.ImageBlock{Data: pngBytes, MediaType: "image/heic"})
	if heic != fallbackImageTokens {
		t.Errorf("heic MediaType must fall back to %d, got %d", fallbackImageTokens, heic)
	}

	// Blank MediaType keeps the sniff so a payload built without the hint
	// is still billed by its pixels when decodable. The empty-ImageBlock
	// case above already covers fall-through to fallback.
	blank := EstimateImage(adapter.ImageBlock{Data: pngBytes}) // MediaType == ""
	if blank != got {
		t.Errorf("blank MediaType sniff should match the registered-MediaType path: %d vs %d", blank, got)
	}
}

// TestEstimateTokens_CountsImages is the regression guard for the trigger
// bug: a message whose weight is entirely images used to estimate at ~0, so
// neither compaction threshold could fire no matter how many accumulated.
func TestEstimateTokens_CountsImages(t *testing.T) {
	msg := adapter.Message{
		Role: adapter.RoleTool,
		Images: []adapter.ImageBlock{
			{Data: encodePNG(t, 800, 600), MediaType: "image/png"},
			{Data: encodePNG(t, 800, 600), MediaType: "image/png"},
		},
	}
	got := EstimateTokens([]adapter.Message{msg})
	if got == 0 {
		t.Fatal("image-only message estimated at 0 tokens — compaction can never trigger")
	}
	// 800×600 = 480k px / 750 = 640 tokens each.
	if got < 1200 || got > 1400 {
		t.Errorf("two 800x600 images = %d tokens, want ~1280", got)
	}
	// EstimateMessage and EstimateTokens must not drift apart.
	if per := EstimateMessage(msg); per != got {
		t.Errorf("EstimateMessage=%d disagrees with EstimateTokens=%d", per, got)
	}
}

// TestSplitMessages_CountsImages: the /context system/conversation split
// walked chars only, so images were missing from that breakdown too. The
// sum-matches-total invariant is covered by
// TestSplitMessages_SumMatchesEstimateTokens.
func TestSplitMessages_CountsImages(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleTool, Images: []adapter.ImageBlock{
			{Data: encodePNG(t, 800, 600), MediaType: "image/png"},
		}},
	}
	sys, convo := SplitMessages(msgs)
	if convo < 600 {
		t.Errorf("conversation bucket = %d tokens, want ~640 from the image", convo)
	}
	if sys+convo != EstimateTokens(msgs) {
		t.Errorf("split sum %d != total %d", sys+convo, EstimateTokens(msgs))
	}
}
