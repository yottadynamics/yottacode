package agent

import (
	"bytes"
	"unicode/utf8"
)

// Helpers shared by the read tools (read_file, read_many_files) for deciding
// whether a window of bytes can be shown as text and for cutting one cleanly.

// trimPartialRune drops an incomplete multi-byte sequence from the end of b.
// Bytes that are simply invalid are left alone for the text check to report.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}

// nonTextReason reports why data should not be shown as text, or "" if it
// should. A byte offset may land inside a multi-byte rune, so up to three
// leading continuation bytes are forgiven when mayStartMidRune is set.
func nonTextReason(data []byte, mayStartMidRune bool) string {
	if bytes.IndexByte(data, 0) >= 0 {
		return "contains NUL bytes"
	}
	check := data
	if mayStartMidRune {
		for i := 0; i < utf8.UTFMax-1 && len(check) > 0 && isUTF8Continuation(check[0]); i++ {
			check = check[1:]
		}
	}
	if !utf8.Valid(check) {
		return "invalid UTF-8"
	}
	return ""
}

// isUTF8Continuation identifies a UTF-8 continuation byte. Invalid leading
// bytes must not be forgiven merely because a later byte starts a rune.
func isUTF8Continuation(b byte) bool {
	return b&0xc0 == 0x80
}

// spanEndsWithNewline reports whether a receipt's byte span ends with a
// newline. Rendered lines never show their terminator, so without this a model
// cannot tell "a\nb" from "a\nb\n" and would have to guess whether old ends in
// a newline.
func spanEndsWithNewline(span []byte) bool {
	return len(span) > 0 && span[len(span)-1] == '\n'
}

// nonTextSkipMessage is what a read tool returns in place of content it
// refuses to show. It is deliberately not an error: the file was found and
// read, it just is not text.
func nonTextSkipMessage(reason string) string {
	return "[skipped: not UTF-8 text (" + reason + ")]"
}
