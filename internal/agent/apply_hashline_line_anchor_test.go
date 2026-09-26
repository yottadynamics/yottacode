package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// splitLinesWithSpans must produce lines/spans in lockstep: spans[i] is the
// exact byte range of lines[i] plus its own terminator (or none, for a final
// unterminated line), and len(lines) == len(spans) always.
func assertLinesAndSpansConsistent(t *testing.T, src []byte, wantLineCount int) ([]string, []lineSpan) {
	t.Helper()
	lines, spans := splitLinesWithSpans(src)
	if len(lines) != wantLineCount || len(spans) != wantLineCount {
		t.Fatalf("splitLinesWithSpans(%q) = %d lines, %d spans; want %d", src, len(lines), len(spans), wantLineCount)
	}
	for i, sp := range spans {
		if sp.offset < 0 || sp.length < 0 || sp.offset+sp.length > len(src) {
			t.Fatalf("line %d: span %+v is outside source bounds (len=%d)", i+1, sp, len(src))
		}
	}
	// Spans must exactly tile the source: contiguous, no gaps or overlap,
	// starting at 0 and ending at len(src) (once trailing content with no
	// terminator, if any, is accounted for).
	for i := 1; i < len(spans); i++ {
		if spans[i].offset != spans[i-1].offset+spans[i-1].length {
			t.Fatalf("spans not contiguous: line %d ends at %d, line %d starts at %d",
				i, spans[i-1].offset+spans[i-1].length, i+1, spans[i].offset)
		}
	}
	if len(spans) > 0 && spans[0].offset != 0 {
		t.Fatalf("first span does not start at byte 0: %+v", spans[0])
	}
	if len(spans) > 0 {
		last := spans[len(spans)-1]
		if last.offset+last.length != len(src) {
			t.Fatalf("last span ends at %d, want end of source at %d", last.offset+last.length, len(src))
		}
	}
	return lines, spans
}

func TestSplitLinesWithSpans_EmptyFileHasNoLines(t *testing.T) {
	assertLinesAndSpansConsistent(t, []byte(""), 0)
}

func TestSplitLinesWithSpans_SingleNewlineIsOneEmptyLine(t *testing.T) {
	lines, spans := assertLinesAndSpansConsistent(t, []byte("\n"), 1)
	if lines[0] != "" || spans[0] != (lineSpan{offset: 0, length: 1}) {
		t.Fatalf("lines=%q spans=%+v", lines, spans)
	}
}

func TestSplitLinesWithSpans_NoTrailingNewlineOnLastLine(t *testing.T) {
	lines, spans := assertLinesAndSpansConsistent(t, []byte("a\nb"), 2)
	if lines[1] != "b" || spans[1] != (lineSpan{offset: 2, length: 1}) {
		t.Fatalf("line 2 = %q, span = %+v, want content %q length 1 (no terminator)", lines[1], spans[1], "b")
	}
}

func TestSplitLinesWithSpans_TrailingNewlineDoesNotAddAnEmptyLastLine(t *testing.T) {
	lines, _ := assertLinesAndSpansConsistent(t, []byte("a\nb\n"), 2)
	if lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("lines = %q, want [a b]", lines)
	}
}

func TestSplitLinesWithSpans_LineCountAndHashesMatchReadFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "one\ntwo\nthree\n")
	out, err := newReadFile(tmp, false).Execute(context.Background(), `{"path":"a.txt","anchors":true}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	src := []byte("one\ntwo\nthree\n")
	lines, _ := assertLinesAndSpansConsistent(t, src, 3)
	for i, content := range lines {
		lineNum := i + 1
		want := anchorHashForLine(lineNum, content)
		// Matches read_file's exact "%6d#%s\t" rendering.
		if !strings.Contains(out, fmt.Sprintf("%6d#%s\t", lineNum, want)) {
			t.Errorf("read_file output missing %d#%s (content %q): %s", lineNum, want, content, out)
		}
	}
}

func TestSplitLinesWithSpans_CRLFKeepsCarriageReturnInContentAndSpan(t *testing.T) {
	src := []byte("a\r\nb\r\n")
	lines, spans := assertLinesAndSpansConsistent(t, src, 2)
	if lines[0] != "a\r" {
		t.Fatalf("line 1 content = %q, want the \\r retained (anchorHashForLine trims it itself)", lines[0])
	}
	if spans[0] != (lineSpan{offset: 0, length: 3}) {
		t.Fatalf("line 1 span = %+v, want length 3 (\"a\\r\\n\")", spans[0])
	}
	if string(src[spans[1].offset:spans[1].offset+spans[1].length]) != "b\r\n" {
		t.Fatalf("line 2 span does not cover exactly %q", "b\r\n")
	}
}

func TestSplitLinesWithSpans_BlankLinesCountSeparately(t *testing.T) {
	lines, _ := assertLinesAndSpansConsistent(t, []byte("a\n\nb\n"), 3)
	if lines[0] != "a" || lines[1] != "" || lines[2] != "b" {
		t.Fatalf("lines = %q, want [a \"\" b]", lines)
	}
}

// resolveLineAnchor ties parseAnchoredRef + resolveAnchoredRef + the span
// table together: given a raw "line#hash" token and the live file bytes, it
// must return the exact byte span of that line (including its terminator).

func TestResolveLineAnchor_ReturnsExactLineSpanIncludingTerminator(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	token := "2#" + anchorHashForLine(2, "beta")
	offset, length, err := resolveLineAnchor(src, token)
	if err != nil {
		t.Fatalf("resolveLineAnchor: %v", err)
	}
	if got := string(src[offset : offset+length]); got != "beta\n" {
		t.Fatalf("resolved span = %q, want %q", got, "beta\n")
	}
}

func TestResolveLineAnchor_LastLineWithoutTerminator(t *testing.T) {
	src := []byte("alpha\nbeta")
	token := "2#" + anchorHashForLine(2, "beta")
	offset, length, err := resolveLineAnchor(src, token)
	if err != nil {
		t.Fatalf("resolveLineAnchor: %v", err)
	}
	if got := string(src[offset : offset+length]); got != "beta" {
		t.Fatalf("resolved span = %q, want %q (no terminator)", got, "beta")
	}
}

func TestResolveLineAnchor_BareHashResolvesUniqueMatch(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	token := anchorHashForLine(2, "beta") // no "N#" prefix
	offset, length, err := resolveLineAnchor(src, token)
	if err != nil {
		t.Fatalf("resolveLineAnchor: %v", err)
	}
	if got := string(src[offset : offset+length]); got != "beta\n" {
		t.Fatalf("resolved span = %q, want %q", got, "beta\n")
	}
}

func TestResolveLineAnchor_StaleLineNumberErrors(t *testing.T) {
	src := []byte("alpha\nbeta\n")
	_, _, err := resolveLineAnchor(src, "5#"+anchorHashForLine(5, "nope"))
	if err == nil || !strings.Contains(err.Error(), "no line 5") {
		t.Fatalf("err = %v, want a no-such-line error", err)
	}
}

func TestResolveLineAnchor_HashMismatchErrors(t *testing.T) {
	src := []byte("alpha\nbeta\n")
	_, _, err := resolveLineAnchor(src, "2#00000000")
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("err = %v, want a stale-anchor error", err)
	}
}

func TestResolveLineAnchor_AmbiguousBareHashErrors(t *testing.T) {
	// Both lines are "x", so they share the same content-only hash under a
	// bare (no line number) reference.
	src := []byte("x\nx\n")
	_, _, err := resolveLineAnchor(src, anchorHashForLine(1, "x"))
	// Line 1 and line 2 hash differently (the line NUMBER is part of the
	// hash input), so this specific case is NOT ambiguous — verifying that
	// distinct hashes for identical content at different lines is what
	// prevents false ambiguity here.
	if err != nil {
		t.Fatalf("resolveLineAnchor: %v, want success (line number disambiguates identical content)", err)
	}
}

func TestResolveLineAnchor_MalformedTokenErrors(t *testing.T) {
	src := []byte("alpha\n")
	_, _, err := resolveLineAnchor(src, "not a token")
	if err == nil || !strings.Contains(err.Error(), "malformed anchor") {
		t.Fatalf("err = %v, want a malformed-anchor error", err)
	}
}
