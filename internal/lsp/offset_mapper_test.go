package lsp

import (
	"strings"
	"testing"
)

// TestOffsetMapperMatchesPositionForOffsetEverywhere pins the optimized mapper to
// the original scan: same position for every offset, and an error in exactly the
// same cases (outside the document, past invalid UTF-8, inside a multi-byte rune).
func TestOffsetMapperMatchesPositionForOffsetEverywhere(t *testing.T) {
	texts := map[string]string{
		"empty":             "",
		"one line":          "package main",
		"trailing newline":  "a\nbb\nccc\n",
		"no trailing":       "a\nbb\nccc",
		"blank lines":       "\n\n\nx\n\n",
		"crlf":              "one\r\ntwo\r\nthree\r\n",
		"two byte":          "café = 1\nnaïve\n",
		"cjk":               "// 日本語\nfunc f() {}\n",
		"astral":            "x := \"\U0001F600\" // \U0001F680\ny := 2\n",
		"invalid at start":  "\xff\nabc\n",
		"invalid in middle": "abc\n\xc3(def\nghi\n",
		"invalid at end":    "abc\ndef\xe2\x82",
		"lone newline":      "\n",
		"long":              strings.Repeat("line of text é\n", 200),
	}
	for name, text := range texts {
		m := newOffsetMapper(text)
		for target := -1; target <= len(text)+1; target++ {
			want, wantErr := PositionForOffset(text, target)
			got, gotErr := m.position(target)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("%s offset %d: error mismatch: PositionForOffset err=%v, mapper err=%v", name, target, wantErr, gotErr)
			}
			if wantErr != nil {
				if wantErr.Error() != gotErr.Error() {
					t.Fatalf("%s offset %d: error text %q vs %q", name, target, wantErr, gotErr)
				}
				continue
			}
			if got != want {
				t.Fatalf("%s offset %d: mapper %+v, PositionForOffset %+v", name, target, got, want)
			}
		}
	}
}

func BenchmarkPositionsOverLargeFile(b *testing.B) {
	text := strings.Repeat("func Example() int { return 1 }\n", 40000)
	offsets := make([]int, 0, 4000)
	for i := 0; i < 4000; i++ {
		offsets = append(offsets, i*len(text)/4000)
	}
	b.Run("PositionForOffset", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, o := range offsets {
				_, _ = PositionForOffset(text, o)
			}
		}
	})
	b.Run("offsetMapper", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			m := newOffsetMapper(text)
			for _, o := range offsets {
				_, _ = m.position(o)
			}
		}
	})
}
