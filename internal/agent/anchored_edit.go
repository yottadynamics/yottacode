package agent

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

const anchoredEditHashHexLen = 8

type anchoredLine struct {
	LineNumber int
	Hash       string
	Content    string
	// RelocatedFrom is the anchor's original line number when resolution had to
	// relocate a stale anchor to this line; zero for an exact match.
	RelocatedFrom int
}

type anchoredRef struct {
	LineNumber int
	Hash       string
	Raw        string
}

type anchoredLineIndex struct {
	ByLine map[int]anchoredLine
	ByHash map[string][]anchoredLine
}

func buildAnchoredLines(lines []string, startLine int) []anchoredLine {
	out := make([]anchoredLine, 0, len(lines))
	for i, line := range lines {
		lineNum := startLine + i
		out = append(out, anchoredLine{
			LineNumber: lineNum,
			Hash:       anchorHashForLine(lineNum, line),
			Content:    line,
		})
	}
	return out
}

func buildAnchoredLineIndex(lines []string) anchoredLineIndex {
	anchored := buildAnchoredLines(lines, 1)
	idx := anchoredLineIndex{
		ByLine: make(map[int]anchoredLine, len(anchored)),
		ByHash: make(map[string][]anchoredLine),
	}
	for _, line := range anchored {
		idx.ByLine[line.LineNumber] = line
		idx.ByHash[line.Hash] = append(idx.ByHash[line.Hash], line)
	}
	return idx
}

func anchorHashForLine(lineNumber int, line string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.Itoa(lineNumber)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSuffix(line, "\r")))
	sum := h.Sum64()
	hex := fmt.Sprintf("%016x", sum)
	return hex[:anchoredEditHashHexLen]
}

func parseAnchoredRef(raw string) (anchoredRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return anchoredRef{}, fmt.Errorf("anchor is required")
	}
	parts := strings.SplitN(raw, "#", 2)
	if len(parts) == 2 {
		lineNum, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || lineNum < 1 {
			return anchoredRef{}, fmt.Errorf("invalid anchor line number %q", parts[0])
		}
		hash := strings.TrimSpace(parts[1])
		if hash == "" {
			return anchoredRef{}, fmt.Errorf("invalid anchor %q: missing hash", raw)
		}
		if !isAnchorHashShape(hash) {
			return anchoredRef{}, fmt.Errorf("malformed anchor %q — the text after '#' must be the exact %d-character hex hash printed by read_file(anchors=true) (e.g. \"42#a1b2c3d4\"), not source text", raw, anchoredEditHashHexLen)
		}
		return anchoredRef{LineNumber: lineNum, Hash: hash, Raw: raw}, nil
	}
	// No "#": treated as a bare hash reference. The dominant mistake here is
	// a model pasting raw source text (a comment, a line of code) instead of
	// the anchor token — that would otherwise sail through to resolveAnchoredRef
	// and surface as a confusing "no current line matches this anchor hash",
	// which reads like the file changed rather than like a format mistake.
	if !isAnchorHashShape(raw) {
		return anchoredRef{}, fmt.Errorf("malformed anchor %q — expected the exact line#hash token printed by read_file(anchors=true) (e.g. \"42#a1b2c3d4\"), not raw source text", raw)
	}
	return anchoredRef{Hash: raw, Raw: raw}, nil
}

// isAnchorHashShape reports whether s has the shape of a hash this package
// generates (anchorHashForLine): a fixed-length lowercase hex string. It
// cannot confirm a hash is genuine, but it reliably rejects the common
// mistake of passing arbitrary text where a hash token belongs.
func isAnchorHashShape(s string) bool {
	if len(s) != anchoredEditHashHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// anchorRelocateWindow bounds how far from its original line number a stale
// anchor may be relocated. Edits earlier in a file shift later lines by a small
// amount; a match far away is more likely a coincidental duplicate than the
// line the caller saw.
const anchorRelocateWindow = 100

// anchorStaleContextRadius is how many lines either side of a stale anchor's
// line are echoed (with fresh anchors) in the stale error, so the caller can
// retry without another read_file.
const anchorStaleContextRadius = 2

const anchorStaleContextMaxLine = 160

// relocateAnchor finds the line a stale anchor originally pointed at after
// lines shifted. The hash is salted with the line number, so the same text at
// a new position has a different hash; instead each nearby line's content is
// re-hashed under the anchor's ORIGINAL line number. It succeeds only on
// exactly one match within anchorRelocateWindow, so a repeated line never
// relocates.
func relocateAnchor(idx anchoredLineIndex, ref anchoredRef) (anchoredLine, bool) {
	var found anchoredLine
	n := 0
	for ln := ref.LineNumber - anchorRelocateWindow; ln <= ref.LineNumber+anchorRelocateWindow; ln++ {
		line, ok := idx.ByLine[ln]
		if !ok {
			continue
		}
		if anchorHashForLine(ref.LineNumber, line.Content) == ref.Hash {
			found = line
			found.RelocatedFrom = ref.LineNumber
			n++
			if n > 1 {
				return anchoredLine{}, false
			}
		}
	}
	return found, n == 1
}

// staleAnchorContext renders the current lines around lineNum as
// "N#hash<TAB>text" so a stale-anchor error carries usable fresh anchors.
func staleAnchorContext(idx anchoredLineIndex, lineNum int) string {
	var b strings.Builder
	for ln := lineNum - anchorStaleContextRadius; ln <= lineNum+anchorStaleContextRadius; ln++ {
		line, ok := idx.ByLine[ln]
		if !ok {
			continue
		}
		text := line.Content
		if len(text) > anchorStaleContextMaxLine {
			text = text[:anchorStaleContextMaxLine] + "…"
		}
		fmt.Fprintf(&b, "\n  %d#%s\t%s", line.LineNumber, line.Hash, text)
	}
	return b.String()
}

func resolveAnchoredRef(idx anchoredLineIndex, ref anchoredRef) (anchoredLine, error) {
	if ref.LineNumber > 0 {
		line, ok := idx.ByLine[ref.LineNumber]
		if !ok {
			if moved, found := relocateAnchor(idx, ref); found {
				return moved, nil
			}
			return anchoredLine{}, fmt.Errorf("stale anchor %s — current file has no line %d", ref.Raw, ref.LineNumber)
		}
		if line.Hash != ref.Hash {
			if moved, found := relocateAnchor(idx, ref); found {
				return moved, nil
			}
			return anchoredLine{}, fmt.Errorf("stale anchor %s — current line %d has %d#%s; the file changed since your read. Current lines near %d (fresh anchors):%s", ref.Raw, ref.LineNumber, line.LineNumber, line.Hash, ref.LineNumber, staleAnchorContext(idx, ref.LineNumber))
		}
		return line, nil
	}
	matches := idx.ByHash[ref.Hash]
	if len(matches) == 0 {
		return anchoredLine{}, fmt.Errorf("stale anchor %s — no current line matches this anchor hash", ref.Raw)
	}
	if len(matches) > 1 {
		lines := make([]string, 0, len(matches))
		for _, match := range matches {
			lines = append(lines, strconv.Itoa(match.LineNumber))
		}
		return anchoredLine{}, fmt.Errorf("bare anchor %s is ambiguous at lines %s; use full line#anchor references", ref.Raw, strings.Join(lines, ", "))
	}
	return matches[0], nil
}
