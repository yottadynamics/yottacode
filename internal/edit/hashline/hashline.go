// Package hashline applies edits guarded by content hashes instead of trusting
// stale offsets alone. It is intentionally exact by default: a hunk only applies
// when its old bytes hash to the recorded anchor hash and the same span can be
// found unambiguously in the current source.
package hashline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"unicode/utf8"
)

const (
	// HashHexLength keeps anchors compact while preserving enough entropy for
	// local edit validation. The full SHA-256 is still used before truncation.
	HashHexLength = 16

	// RelocationWindow bounds stale-offset recovery so the matcher stays cheap and
	// does not wander through unrelated repeated code looking for a plausible fit.
	RelocationWindow = 64 * 1024

	// rereadContextBytes gives callers enough nearby context to refresh a stale
	// anchor without dumping the whole file back into the model context.
	rereadContextBytes = 256
)

// Anchor records the exact byte span observed at read time.
type Anchor struct {
	Path   string
	Offset int
	Length int
	Hash   string // hex sha256(normalized_span)[:16]
}

// Hunk replaces Old bytes at Anchor with New bytes after validating the anchor.
type Hunk struct {
	Anchor Anchor
	Old    []byte // must hash to Anchor.Hash
	New    []byte
}

// ErrorKind identifies structured edit failures for model-facing recovery loops.
type ErrorKind string

const (
	ErrStaleAnchor     ErrorKind = "stale_anchor"
	ErrAmbiguousAnchor ErrorKind = "ambiguous_anchor"
	ErrOverlap         ErrorKind = "overlapping_hunks"
	ErrInvalidText     ErrorKind = "invalid_text"
	ErrInvalidRange    ErrorKind = "invalid_range"
)

// ApplyError carries structured context while still formatting as a plain error
// for tools that only expose textual failures.
type ApplyError struct {
	Kind         ErrorKind
	Message      string
	ExpectedHash string
	FoundHash    string
	RereadStart  int
	RereadEnd    int
}

func (e *ApplyError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = string(e.Kind)
	} else if e.Kind != "" {
		msg = string(e.Kind) + ": " + msg
	}
	if e.ExpectedHash != "" || e.FoundHash != "" {
		msg += fmt.Sprintf(" (expected_hash=%s found_hash=%s)", e.ExpectedHash, e.FoundHash)
	}
	if e.RereadEnd > e.RereadStart {
		msg += fmt.Sprintf("; re-read bytes %d:%d and retry", e.RereadStart, e.RereadEnd)
	}
	return msg
}

// HashSpan hashes the exact byte span at offset:length and returns a compact
// anchor. No whitespace normalization is applied; Go and many other languages
// are whitespace-sensitive, so fuzzy matching must be opt-in in a later layer.
func HashSpan(src []byte, offset, length int) (Anchor, error) {
	if offset < 0 || length < 0 || offset > len(src) || length > len(src)-offset {
		return Anchor{}, &ApplyError{
			Kind:        ErrInvalidRange,
			Message:     "hash span is outside source bounds",
			RereadStart: clamp(offset-rereadContextBytes, 0, len(src)),
			RereadEnd:   clamp(offset+max(length, 1)+rereadContextBytes, 0, len(src)),
		}
	}
	return Anchor{Offset: offset, Length: length, Hash: hashBytes(src[offset : offset+length])}, nil
}

// Apply validates every hunk against src and returns a new byte slice with all
// edits applied. The input slice is never modified.
func Apply(src []byte, hunks []Hunk) ([]byte, error) {
	if !isText(src) {
		return nil, &ApplyError{Kind: ErrInvalidText, Message: "source is not valid UTF-8 text"}
	}
	resolved := make([]resolvedHunk, 0, len(hunks))
	for i, hunk := range hunks {
		if !isText(hunk.Old) || !isText(hunk.New) {
			return nil, &ApplyError{Kind: ErrInvalidText, Message: fmt.Sprintf("hunk %d contains non-text bytes", i)}
		}
		if hashBytes(hunk.Old) != hunk.Anchor.Hash {
			rereadStart, rereadEnd := rereadRange(src, hunk.Anchor)
			return nil, &ApplyError{
				Kind:         ErrStaleAnchor,
				Message:      fmt.Sprintf("hunk %d old bytes do not match anchor hash", i),
				ExpectedHash: hunk.Anchor.Hash,
				FoundHash:    hashBytes(hunk.Old),
				RereadStart:  rereadStart,
				RereadEnd:    rereadEnd,
			}
		}
		start, err := resolveHunk(src, hunk)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, resolvedHunk{index: i, start: start, end: start + hunk.Anchor.Length, new: hunk.New})
	}
	if err := rejectOverlaps(resolved); err != nil {
		return nil, err
	}

	// Apply from the end of the file toward the start so earlier byte offsets stay
	// valid regardless of insertion and deletion sizes.
	out := append([]byte(nil), src...)
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].start == resolved[j].start {
			return resolved[i].end > resolved[j].end
		}
		return resolved[i].start > resolved[j].start
	})
	for _, hunk := range resolved {
		out = append(out[:hunk.start], append(append([]byte(nil), hunk.new...), out[hunk.end:]...)...)
	}
	return out, nil
}

type resolvedHunk struct {
	index int
	start int
	end   int
	new   []byte
}

func resolveHunk(src []byte, hunk Hunk) (int, error) {
	anchor := hunk.Anchor
	if anchor.Offset < 0 || anchor.Length < 0 || anchor.Offset > len(src) {
		return 0, staleAnchorError(src, anchor, "anchor range is outside current source bounds", "")
	}
	if anchor.Length <= len(src)-anchor.Offset {
		current := src[anchor.Offset : anchor.Offset+anchor.Length]
		found := hashBytes(current)
		if found == anchor.Hash && bytes.Equal(current, hunk.Old) {
			return anchor.Offset, nil
		}
	}

	start := clamp(anchor.Offset-RelocationWindow, 0, len(src))
	end := clamp(anchor.Offset+anchor.Length+RelocationWindow, 0, len(src))
	matches := findMatches(src[start:end], hunk.Old)
	if len(matches) == 1 {
		return start + matches[0], nil
	}
	found := ""
	if anchor.Length <= len(src)-anchor.Offset {
		found = hashBytes(src[anchor.Offset : anchor.Offset+anchor.Length])
	}
	if len(matches) > 1 {
		return 0, &ApplyError{
			Kind:         ErrAmbiguousAnchor,
			Message:      "anchor content appears multiple times near the recorded offset",
			ExpectedHash: anchor.Hash,
			FoundHash:    found,
			RereadStart:  rereadStart(src, anchor),
			RereadEnd:    rereadEnd(src, anchor),
		}
	}
	return 0, staleAnchorError(src, anchor, "anchor content is stale and no unique nearby match was found", found)
}

func rejectOverlaps(hunks []resolvedHunk) error {
	ordered := append([]resolvedHunk(nil), hunks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].start == ordered[j].start {
			return ordered[i].end < ordered[j].end
		}
		return ordered[i].start < ordered[j].start
	})
	for i := 1; i < len(ordered); i++ {
		prev := ordered[i-1]
		cur := ordered[i]
		if prev.end > cur.start {
			return &ApplyError{
				Kind:    ErrOverlap,
				Message: fmt.Sprintf("hunks %d and %d overlap", prev.index, cur.index),
			}
		}
	}
	return nil
}

func findMatches(src, old []byte) []int {
	if len(old) == 0 {
		return []int{0}
	}
	var matches []int
	search := src
	base := 0
	for {
		idx := bytes.Index(search, old)
		if idx < 0 {
			return matches
		}
		matches = append(matches, base+idx)
		base += idx + 1
		search = search[idx+1:]
	}
}

func staleAnchorError(src []byte, anchor Anchor, msg, found string) error {
	return &ApplyError{
		Kind:         ErrStaleAnchor,
		Message:      msg,
		ExpectedHash: anchor.Hash,
		FoundHash:    found,
		RereadStart:  rereadStart(src, anchor),
		RereadEnd:    rereadEnd(src, anchor),
	}
}

func hashBytes(src []byte) string {
	sum := sha256.Sum256(src)
	encoded := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(encoded, sum[:])
	return string(encoded[:HashHexLength])
}

func isText(src []byte) bool {
	return utf8.Valid(src) && !bytes.Contains(src, []byte{0})
}

func rereadStart(src []byte, anchor Anchor) int {
	start, _ := rereadRange(src, anchor)
	return start
}

func rereadEnd(src []byte, anchor Anchor) int {
	_, end := rereadRange(src, anchor)
	return end
}

func rereadRange(src []byte, anchor Anchor) (int, int) {
	length := anchor.Length
	if length < 1 {
		length = 1
	}
	start := clamp(anchor.Offset-rereadContextBytes, 0, len(src))
	end := clamp(anchor.Offset+length+rereadContextBytes, 0, len(src))
	if end <= start && len(src) > 0 {
		end = clamp(start+1, 0, len(src))
	}
	return start, end
}

func clamp(n, low, high int) int {
	if n < low {
		return low
	}
	if n > high {
		return high
	}
	return n
}
