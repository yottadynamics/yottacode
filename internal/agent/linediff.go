package agent

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// boundedDiffMaxBytes bounds the whole rendered diff, whatever the line
	// lengths, so a one-byte edit beside a minified 1 MiB line stays small.
	boundedDiffMaxBytes = 16 * 1024

	// boundedDiffMaxLineBytes clips each rendered line; the ellipsis shows it
	// was clipped.
	boundedDiffMaxLineBytes = 200

	// boundedDiffMaxEdits caps the edit distance Myers' algorithm will search
	// for (its trace costs O(edits²) memory). A change bigger than this is a
	// rewrite, and is reported as one coarse hunk instead.
	boundedDiffMaxEdits = 500

	// boundedDiffMaxMiddle bounds the region between the common prefix and
	// suffix that the diff will even attempt to align.
	boundedDiffMaxMiddle = 50_000

	diffTruncatedMarker = "…[truncated diff]"
)

// diffOp is one line of an edit script. oldIdx and newIdx are the number of
// old and new lines that precede the op, so for '=' and '-' the line is
// old[oldIdx], and for '+' it is new[newIdx].
type diffOp struct {
	kind           byte // '=', '-', '+'
	oldIdx, newIdx int
}

// diffHunk is one @@ block. The counts are the true size of the block, which
// can exceed len(ops) when a coarse hunk lists only its first lines.
type diffHunk struct {
	ops                []diffOp
	oldStart, oldCount int
	newStart, newCount int
	omitted            bool // ops is a truncated view of the block
}

// boundedUnifiedDiff renders a unified-style diff of oldText to newText for
// model- and human-facing previews. Unlike a real patch it is capped: at most
// maxChangedLines -/+ lines and boundedDiffMaxBytes overall, with every line
// clipped, and it always ends in diffTruncatedMarker when anything was left
// out. Distant edits become separate hunks.
func boundedUnifiedDiff(path, oldText, newText string, contextLines, maxChangedLines int) string {
	if oldText == newText {
		return fmt.Sprintf("diff -- %s\n(no changes)\n", path)
	}
	a, b := splitDiffLines(oldText), splitDiffLines(newText)
	hunks := buildDiffHunks(a, b, contextLines, maxChangedLines)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	changed, truncated := 0, false
hunks:
	for _, h := range hunks {
		if changed >= maxChangedLines {
			truncated = true
			break
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount)
		if out.Len()+len(header) > boundedDiffMaxBytes {
			truncated = true
			break
		}
		out.WriteString(header)
		for _, op := range h.ops {
			if op.kind != '=' {
				if changed >= maxChangedLines {
					truncated = true
					break hunks
				}
				changed++
			}
			line := renderDiffLine(op, a, b)
			if out.Len()+len(line) > boundedDiffMaxBytes {
				truncated = true
				break hunks
			}
			out.WriteString(line)
		}
		if h.omitted {
			truncated = true
			break
		}
	}
	if truncated {
		out.WriteString(diffTruncatedMarker + "\n")
	}
	return out.String()
}

// splitDiffLines splits text into lines that keep their terminator, so a final
// line without a newline differs from the same line with one.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// buildDiffHunks aligns a and b and groups the changes into hunks with
// contextLines of context, merging hunks whose context touches.
func buildDiffHunks(a, b []string, contextLines, maxChangedLines int) []diffHunk {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	midA, midB := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]

	var kinds []byte
	ok := len(midA)+len(midB) <= boundedDiffMaxMiddle
	if ok {
		kinds, ok = myersEdits(midA, midB, boundedDiffMaxEdits)
	}
	if !ok {
		return []diffHunk{coarseDiffHunk(a, b, prefix, suffix, contextLines, maxChangedLines)}
	}

	// Flatten to: leading context, the middle edit script, trailing context.
	var ops []diffOp
	for i := max(prefix-contextLines, 0); i < prefix; i++ {
		ops = append(ops, diffOp{'=', i, i})
	}
	oi, ni := prefix, prefix
	for _, k := range kinds {
		ops = append(ops, diffOp{k, oi, ni})
		if k != '+' {
			oi++
		}
		if k != '-' {
			ni++
		}
	}
	for t := 0; t < min(contextLines, suffix); t++ {
		ops = append(ops, diffOp{'=', oi + t, ni + t})
	}

	// Keep every op within contextLines of a change; hunks are the maximal runs
	// of kept ops, so changes closer than 2*contextLines merge.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == '=' {
			continue
		}
		for j := max(i-contextLines, 0); j <= min(i+contextLines, len(ops)-1); j++ {
			keep[j] = true
		}
	}
	var hunks []diffHunk
	for i := 0; i < len(ops); {
		if !keep[i] {
			i++
			continue
		}
		j := i
		for j < len(ops) && keep[j] {
			j++
		}
		hunks = append(hunks, newDiffHunk(ops[i:j]))
		i = j
	}
	return hunks
}

func newDiffHunk(ops []diffOp) diffHunk {
	h := diffHunk{ops: ops}
	for _, op := range ops {
		if op.kind != '+' {
			h.oldCount++
		}
		if op.kind != '-' {
			h.newCount++
		}
	}
	h.oldStart = hunkStart(ops[0].oldIdx, h.oldCount)
	h.newStart = hunkStart(ops[0].newIdx, h.newCount)
	return h
}

// hunkStart converts a 0-based line index to a unified-diff start. An empty
// range starts at the line before it (so an insertion at the top is line 0).
func hunkStart(idx, count int) int {
	if count == 0 {
		return idx
	}
	return idx + 1
}

// coarseDiffHunk describes a rewrite too large to align as one block: the
// context around it, then the first removed and added lines. The header
// reports the true size; the renderer marks the hunk truncated.
func coarseDiffHunk(a, b []string, prefix, suffix, contextLines, maxChangedLines int) diffHunk {
	var ops []diffOp
	for i := max(prefix-contextLines, 0); i < prefix; i++ {
		ops = append(ops, diffOp{'=', i, i})
	}
	midA, midB := len(a)-prefix-suffix, len(b)-prefix-suffix
	for t := 0; t < min(midA, maxChangedLines+1); t++ {
		ops = append(ops, diffOp{'-', prefix + t, prefix})
	}
	for t := 0; t < min(midB, maxChangedLines+1); t++ {
		ops = append(ops, diffOp{'+', prefix + midA, prefix + t})
	}
	ctxBefore, ctxAfter := min(contextLines, prefix), min(contextLines, suffix)
	h := diffHunk{
		ops:      ops,
		oldCount: ctxBefore + midA + ctxAfter,
		newCount: ctxBefore + midB + ctxAfter,
		omitted:  true,
	}
	h.oldStart = hunkStart(prefix-ctxBefore, h.oldCount)
	h.newStart = hunkStart(prefix-ctxBefore, h.newCount)
	return h
}

// renderDiffLine renders one op as a diff line, clipped to
// boundedDiffMaxLineBytes, flagging a source line that lacks a final newline.
func renderDiffLine(op diffOp, a, b []string) string {
	var tag string
	var src string
	switch op.kind {
	case '=':
		tag, src = " ", a[op.oldIdx]
	case '-':
		tag, src = "-", a[op.oldIdx]
	default:
		tag, src = "+", b[op.newIdx]
	}
	text := strings.TrimSuffix(src, "\n")
	line := tag + clipDiffText(text) + "\n"
	if !strings.HasSuffix(src, "\n") {
		line += "\\ No newline at end of file\n"
	}
	return line
}

func clipDiffText(s string) string {
	if len(s) <= boundedDiffMaxLineBytes {
		return s
	}
	cut := boundedDiffMaxLineBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// myersEdits returns a shortest edit script turning a into b, as a sequence of
// '=', '-' and '+' (Myers, "An O(ND) Difference Algorithm"). It reports false
// when the script would need more than maxEdits insertions and deletions.
func myersEdits(a, b []string, maxEdits int) ([]byte, bool) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		if n+m > maxEdits {
			return nil, false
		}
		return bytes.Repeat([]byte{map[bool]byte{true: '+', false: '-'}[n == 0]}, n+m), true
	}
	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int, 2*limit+3)
	trace := make([][]int, 0, limit+1)
	for d := 0; d <= limit; d++ {
		// Snapshot the diagonals round d reads: k in [-d-1, d+1].
		snap := make([]int, 2*d+3)
		copy(snap, v[off-d-1:off+d+2])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // step down: insert b[y]
			} else {
				x = v[off+k-1] + 1 // step right: delete a[x-1]
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrackEdits(trace, n, m, d), true
			}
		}
	}
	return nil, false
}

// backtrackEdits walks the recorded trace from (n, m) back to the origin and
// returns the edit script in forward order.
func backtrackEdits(trace [][]int, n, m, edits int) []byte {
	x, y := n, m
	rev := make([]byte, 0, n+m)
	for d := edits; d > 0; d-- {
		snap := trace[d] // diagonal k lives at snap[k+d+1]
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && snap[k-1+d+1] < snap[k+1+d+1]) {
			prevK = k + 1
		}
		prevX := snap[prevK+d+1]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, '=')
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, '+')
			y--
		} else {
			rev = append(rev, '-')
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, '=')
		x--
		y--
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
