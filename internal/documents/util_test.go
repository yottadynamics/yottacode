package documents

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// naiveJoin reproduces the plain strings.Builder concatenation the XML
// and HTML extractors used before boundedTextBuilder existed. Every
// property below is stated against it, because the whole point of the
// change was to cut memory without altering a single byte of output.
func naiveJoin(chunks []string) string {
	var b strings.Builder
	for _, s := range chunks {
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	return b.String()
}

func boundedChunkCases() map[string][]string {
	return map[string][]string{
		"several chunks":     {"hello", "world", "again"},
		"empty chunks":       {"", "a", "", "b"},
		"single chunk":       {"single"},
		"no chunks":          {},
		"one oversize chunk": {strings.Repeat("x", 300), "tail"},
		"multibyte":          {"héllo wörld", "ünïcode ätext"},
	}
}

// TestBoundedTextBuilderMatchesNaiveOutput is the equivalence contract:
// for any chunk sequence and any limit, the preview and the reported
// total must be exactly what the old full-buffering code produced.
func TestBoundedTextBuilderMatchesNaiveOutput(t *testing.T) {
	for _, limit := range []int{1, 5, 12, 20, 1000} {
		for name, chunks := range boundedChunkCases() {
			full := naiveJoin(chunks)

			tb := newBoundedTextBuilder(0, limit)
			for _, s := range chunks {
				tb.Add(s)
			}

			if tb.Total() != len(full) {
				t.Errorf("%s/limit=%d: Total() = %d, want %d (the warning's \"of M characters\" depends on this)",
					name, limit, tb.Total(), len(full))
			}
			want := boundedString(full, limit)
			got := boundedString(tb.String(), limit)
			if got != want {
				t.Errorf("%s/limit=%d: preview = %q, want %q", name, limit, got, want)
			}
		}
	}
}

// TestBoundedTextBuilderCapsRetainedBytes is the fix itself: memory held
// must track the limit, not the document. Before this, a 32 MiB XML file
// buffered all 32 MiB of its visible text to return a 20 KB preview.
func TestBoundedTextBuilderCapsRetainedBytes(t *testing.T) {
	const limit = 50

	tb := newBoundedTextBuilder(0, limit)
	for range 2000 {
		tb.Add(strings.Repeat("x", 100))
	}

	if got := len(tb.String()); got > limit+retainSlack {
		t.Errorf("retained %d bytes, want at most %d — the builder must not grow with the input", got, limit+retainSlack)
	}
	// ...while the discarded remainder is still counted.
	if want := 2000*100 + 1999; tb.Total() != want {
		t.Errorf("Total() = %d, want %d", tb.Total(), want)
	}
}

// TestBoundedTextBuilderSurvivesOneHugeChunk: clipping happens inside a
// chunk, not just between chunks, so a document that is one enormous
// text node can't defeat the cap.
func TestBoundedTextBuilderSurvivesOneHugeChunk(t *testing.T) {
	const limit = 32

	tb := newBoundedTextBuilder(0, limit)
	tb.Add(strings.Repeat("y", 1<<20))

	if got := len(tb.String()); got > limit+retainSlack {
		t.Errorf("retained %d bytes from a single 1 MiB chunk, want at most %d", got, limit+retainSlack)
	}
	if tb.Total() != 1<<20 {
		t.Errorf("Total() = %d, want %d", tb.Total(), 1<<20)
	}
}

// TestBoundedTextBuilderPreviewIsRuneSafe: Add clips on a byte offset
// and may leave a partial rune at the tail, so the final boundedString
// cut has to remove it. A preview containing invalid UTF-8 would render
// as a replacement character in the model's context.
func TestBoundedTextBuilderPreviewIsRuneSafe(t *testing.T) {
	for limit := 1; limit <= 40; limit++ {
		tb := newBoundedTextBuilder(0, limit)
		for range 10 {
			tb.Add("héllo wörld ünïcode")
		}
		preview := boundedString(tb.String(), limit)
		if !utf8.ValidString(preview) {
			t.Errorf("limit=%d: preview is not valid UTF-8: %q", limit, preview)
		}
	}
}

// TestBoundedTextBuilderOffsetWindow: with an offset the retained slice
// must match the same window of the naive full string, and must still
// hold no more than the limit regardless of how far in the window sits.
func TestBoundedTextBuilderOffsetWindow(t *testing.T) {
	chunks := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	full := naiveJoin(chunks)

	for _, limit := range []int{1, 4, 9, 100} {
		for offset := 0; offset <= len(full)+3; offset++ {
			tb := newBoundedTextBuilder(offset, limit)
			for _, s := range chunks {
				tb.Add(s)
			}

			var want string
			if offset < len(full) {
				want = boundedString(full[offset:], limit)
			}
			if got := boundedString(tb.String(), limit); got != want {
				t.Fatalf("offset=%d limit=%d: window = %q, want %q", offset, limit, got, want)
			}
			if got := len(tb.String()); got > limit+retainSlack {
				t.Fatalf("offset=%d limit=%d: retained %d bytes, want at most %d", offset, limit, got, limit+retainSlack)
			}
			if tb.Total() != len(full) {
				t.Fatalf("offset=%d: Total() = %d, want %d regardless of the window", offset, tb.Total(), len(full))
			}
		}
	}
}

// TestBoundedTextBuilderOffsetIsRuneSafe: an offset can land mid-rune,
// which would otherwise start the preview with orphaned continuation
// bytes.
func TestBoundedTextBuilderOffsetIsRuneSafe(t *testing.T) {
	for offset := range 40 {
		tb := newBoundedTextBuilder(offset, 20)
		for range 5 {
			tb.Add("héllo wörld ünïcode")
		}
		if preview := boundedString(tb.String(), 20); !utf8.ValidString(preview) {
			t.Errorf("offset=%d: preview is not valid UTF-8: %q", offset, preview)
		}
	}
}
