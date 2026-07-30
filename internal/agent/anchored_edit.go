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
		return anchoredRef{LineNumber: lineNum, Hash: hash, Raw: raw}, nil
	}
	return anchoredRef{Hash: raw, Raw: raw}, nil
}

func resolveAnchoredRef(idx anchoredLineIndex, ref anchoredRef) (anchoredLine, error) {
	if ref.LineNumber > 0 {
		line, ok := idx.ByLine[ref.LineNumber]
		if !ok {
			return anchoredLine{}, fmt.Errorf("stale anchor %s — current file has no line %d", ref.Raw, ref.LineNumber)
		}
		if line.Hash != ref.Hash {
			return anchoredLine{}, fmt.Errorf("stale anchor %s — current line %d has %d#%s", ref.Raw, ref.LineNumber, line.LineNumber, line.Hash)
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
