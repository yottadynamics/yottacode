package agent

import (
	"bytes"
)

// lineSpan is the exact byte range of one line within a source buffer,
// including that line's own terminator ("\n" or "\r\n") — or none, for a
// final line the file does not end with a newline. Spans tile the source
// exactly: contiguous, no gaps, starting at 0 and ending at len(src).
type lineSpan struct {
	offset int
	length int
}

// splitLinesWithSpans splits src into lines and their byte spans in one pass,
// so the two stay in lockstep by construction: lines[i] is exactly the
// content that spans[i] covers, minus its terminator. Line numbering and
// line content match read_file/read_many_files (and therefore anchorHashForLine)
// exactly — a file ending in "\n" has no trailing empty line, and an empty
// file has zero lines.
func splitLinesWithSpans(src []byte) ([]string, []lineSpan) {
	var lines []string
	var spans []lineSpan
	offset := 0
	for offset <= len(src) {
		idx := bytes.IndexByte(src[offset:], '\n')
		if idx < 0 {
			if offset == len(src) {
				break // no partial final line to emit (empty src, or src ends in "\n")
			}
			lines = append(lines, string(src[offset:]))
			spans = append(spans, lineSpan{offset: offset, length: len(src) - offset})
			break
		}
		length := idx + 1 // include the "\n"
		lines = append(lines, string(src[offset:offset+idx]))
		spans = append(spans, lineSpan{offset: offset, length: length})
		offset += length
	}
	return lines, spans
}

// resolveLineAnchor resolves a "line#hash" (or bare "hash") token — the exact
// format read_file/read_many_files print with anchors=true, and edit_anchored
// accepts — against the current bytes of src, and returns the byte span of
// that line including its own terminator. Errors are plain (wrapped into a
// recoverable *hashline.ApplyError by the caller) and reuse
// parseAnchoredRef/resolveAnchoredRef verbatim, so a malformed, stale, or
// ambiguous anchor is reported identically to edit_anchored.
func resolveLineAnchor(src []byte, rawAnchor string) (offset, length int, err error) {
	ref, err := parseAnchoredRef(rawAnchor)
	if err != nil {
		return 0, 0, err
	}
	lines, spans := splitLinesWithSpans(src)
	idx := buildAnchoredLineIndex(lines)
	line, err := resolveAnchoredRef(idx, ref)
	if err != nil {
		return 0, 0, err
	}
	span := spans[line.LineNumber-1]
	return span.offset, span.length, nil
}
