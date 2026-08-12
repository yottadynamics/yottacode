package documents

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// windowText slices s to the requested window and returns the preview
// plus a label naming it. Any path that returns a text preview must go
// through this: a path that renders s without applying Offset looks
// correct in isolation but returns page 1 forever, so a caller paging
// through the file either loops or silently re-reads the same content.
func windowText(s string, offset, limit int, base string) (text, label string) {
	switch {
	case offset >= len(s):
		return "", base + " (empty — offset is past the end)"
	case offset > 0:
		return boundedString(s[offset:], limit),
			fmt.Sprintf("%s characters %d-%d", base, offset+1, min(offset+limit, len(s)))
	default:
		return boundedString(s, limit), base
	}
}

// textWindowNotice describes which slice of a text preview came back, or
// "" when the whole thing fit and nothing needs saying. Paging makes
// "showing the first N" wrong, so once an offset is in play the message
// names the actual window — and an offset past the end must be called
// out, because an empty preview is otherwise indistinguishable from an
// empty document.
func textWindowNotice(offset, limit, total int, noun string) string {
	switch {
	case total > 0 && offset >= total:
		return fmt.Sprintf("offset %d is past the end of the %s (%d characters)", offset, noun, total)
	case offset == 0 && total <= limit:
		return ""
	case offset == 0:
		return fmt.Sprintf("showing the first %d of %d characters of %s", limit, total, noun)
	default:
		return fmt.Sprintf("showing %s characters %d-%d of %d", noun, offset+1, min(offset+limit, total), total)
	}
}

// utf8BOM is the UTF-8 byte-order mark. Windows Excel writes one on
// every CSV and JSON export by default, and neither encoding/csv nor
// encoding/json treats it as skippable whitespace: in a CSV it becomes
// part of the first column's *name* (so "id" arrives as "<BOM>id" and
// every downstream match on that column fails), and in a JSON document
// it fails the entire parse with `invalid character 'ï'`. In JSONL it
// makes the first record unparseable, which the extractor then counts
// as a malformed line — silently costing the caller record 1.
//
// Removing it loses nothing, so it happens quietly rather than as a
// warning; there is no interpretation in which the mark was content.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripBOMReader consumes a leading UTF-8 BOM from br when present.
func stripBOMReader(br *bufio.Reader) {
	if buf, _ := br.Peek(len(utf8BOM)); bytes.Equal(buf, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
}

// stripBOMBytes is the already-buffered counterpart to stripBOMReader,
// for extractors that read their whole allowance up front.
func stripBOMBytes(b []byte) []byte {
	return bytes.TrimPrefix(b, utf8BOM)
}

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

// boundedTextBuilder accumulates space-separated text chunks while
// holding only a little more than limit bytes in memory, and separately
// tracks the full length the text *would* have reached. That split is
// the point: callers still need the true total to report an honest
// "showing the first N of M characters", which is why the naive fix of
// simply dropping chunks past the cap doesn't work.
//
// Without this, the XML and HTML extractors buffered a document's entire
// visible text just to return the first max_chars of it — waste
// proportional to file size, and newly worth caring about now that
// max_bytes can be raised toward the 32 MiB ceiling.
// retainSlack is how much the builder keeps past its limit. One byte is
// what lets boundedString notice the text overflowed at all; the rest
// covers the partial leading rune String drops when a non-zero offset
// lands mid-character. Without that headroom the trim can pull the
// buffer back under the limit, boundedString then sees a string that
// "fits", and it returns the window with a partial rune still on the
// tail.
const retainSlack = 1 + utf8.UTFMax

type boundedTextBuilder struct {
	b      strings.Builder
	offset int
	limit  int
	total  int
}

// newBoundedTextBuilder retains the window [offset, offset+limit) of the
// text it is fed, plus retainSlack bytes so the caller can still detect
// overflow after any rune trimming.
func newBoundedTextBuilder(offset, limit int) *boundedTextBuilder {
	return &boundedTextBuilder{offset: offset, limit: limit}
}

// Add appends one chunk, separated from the previous by a single space
// (matching what a plain strings.Builder concatenation would produce).
// Empty chunks are ignored and do not consume a separator.
func (t *boundedTextBuilder) Add(s string) {
	if s == "" {
		return
	}
	if t.total > 0 {
		t.write(" ")
	}
	t.write(s)
}

// write advances the absolute position by len(s) while retaining only
// the part of s that lands inside the window. Clipping mid-chunk (rather
// than accepting or dropping whole chunks) is what keeps a document that
// is one enormous text node from defeating the cap.
func (t *boundedTextBuilder) write(s string) {
	start := t.total
	t.total += len(s)

	end := t.offset + t.limit + retainSlack
	if start >= end || t.total <= t.offset {
		return // entirely before or after the window
	}
	lo := max(t.offset-start, 0)
	hi := len(s)
	if t.total > end {
		hi -= t.total - end
	}
	if lo < hi {
		t.b.WriteString(s[lo:hi])
	}
}

// Total is the length of the full text, counting everything outside the
// window that was never retained.
func (t *boundedTextBuilder) Total() int { return t.total }

// String returns the retained window. A non-zero offset can land
// mid-rune, so any partial leading rune is dropped here; a partial
// trailing one is removed by the boundedString cut the caller applies.
func (t *boundedTextBuilder) String() string {
	s := t.b.String()
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// buildLabeledUnitSections turns units (already the requested [startNum,
// startNum+len(units)) range of discrete document units — PDF pages,
// pptx slides) into labeled sections, stopping once the running total
// would exceed maxChars — same budget-stop shape as formatCSVRows, so a
// single oversized unit is truncated via boundedString rather than
// silently dropping the rest of the document. noun labels each section
// ("page", "slide") and the warnings it produces.
func buildLabeledUnitSections(units []string, startNum, maxChars int, noun string) ([]DocumentSection, []string) {
	var sections []DocumentSection
	var warnings []string
	used := 0
	for i, text := range units {
		num := startNum + i
		remaining := maxChars - used
		if remaining <= 0 {
			warnings = append(warnings, fmt.Sprintf("stopped at the %d-character preview cap after %s %d", maxChars, noun, num-1))
			break
		}
		body := text
		if len(body) > remaining {
			body = boundedString(body, remaining)
			sections = append(sections, DocumentSection{Label: fmt.Sprintf("%s %d", noun, num), Text: body})
			warnings = append(warnings, fmt.Sprintf("%s %d truncated at the %d-character preview cap", noun, num, maxChars))
			break
		}
		sections = append(sections, DocumentSection{Label: fmt.Sprintf("%s %d", noun, num), Text: body})
		used += len(body)
	}
	return sections, warnings
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
