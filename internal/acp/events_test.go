package acp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateOneLine_DoesNotSplitMultiByteRune is the regression test
// for a real bug: truncateOneLine's doc contract is "max chars", but it
// sliced the byte-indexed string (out[:max]) instead of the rune slice
// it had just built, so a max cutting through a multi-byte UTF-8
// character (e.g. "…", CJK text, emoji) produced invalid UTF-8 — bytes
// that decode as utf8.RuneError once written out over session/update.
func TestTruncateOneLine_DoesNotSplitMultiByteRune(t *testing.T) {
	// "中" is 3 bytes in UTF-8; max=1 must keep the whole rune, not one
	// byte of it.
	got := truncateOneLine("中文测试", 1)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateOneLine produced invalid UTF-8: %q (bytes %v)", got, []byte(got))
	}
	if !strings.HasPrefix(got, "中") {
		t.Errorf("truncateOneLine(%q, 1) = %q, want to start with the first full rune 中", "中文测试", got)
	}
}

func TestTruncateOneLine_ReplacesNewlines(t *testing.T) {
	got := truncateOneLine("line one\nline two\r\nline three", 100)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("truncateOneLine left a newline in the output: %q", got)
	}
}

func TestTruncateOneLine_ShortStringUnchanged(t *testing.T) {
	got := truncateOneLine("short", 100)
	if got != "short" {
		t.Errorf("truncateOneLine(%q, 100) = %q, want unchanged", "short", got)
	}
}

func TestTruncateOneLine_TruncatesByRuneCountNotByteCount(t *testing.T) {
	// 5 runes, each 3 bytes (15 bytes total) — max=3 (runes) must keep
	// exactly 3 runes plus the ellipsis, not stop mid-rune at byte 3.
	got := truncateOneLine("中文测试例", 3)
	want := "中文测…"
	if got != want {
		t.Errorf("truncateOneLine(%q, 3) = %q, want %q", "中文测试例", got, want)
	}
}
