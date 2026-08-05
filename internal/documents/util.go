package documents

import (
	"io"
	"unicode/utf8"
)

// boundedString truncates s to at most maxChars bytes, backing off to a
// valid UTF-8 rune boundary rather than splitting a multi-byte
// character, and appends a truncation marker when it cuts anything.
func boundedString(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	cut := maxChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// hasMoreAfterCap reports whether r has at least one more byte
// available beyond whatever a caller already consumed through
// io.LimitReader(r, cap). Call this only after that limited read has
// fully finished (the parser/decoder hit EOF from the limiter), so r's
// read position sits exactly at the cap — or earlier, if the source
// was shorter than the cap.
//
// Probing the source directly this way, AFTER the capped read is done,
// is deliberately different from reading one extra byte THROUGH the
// limiter (i.e. io.LimitReader(r, cap+1)): that approach leaks the
// extra byte's content into whatever the parser produced (a truncated
// CSV row could pick up one more real byte and parse as complete when
// it shouldn't have), whereas probing afterward never hands that byte
// to the parser at all. It's also more reliable than comparing a
// separately-stat'd file size against the cap, which can go stale if
// the file changes between the stat and the read.
func hasMoreAfterCap(r io.Reader) bool {
	var probe [1]byte
	n, _ := r.Read(probe[:])
	return n > 0
}
