package lsp

import (
	"strings"
	"testing"
)

func TestApplyTextEditsAppliesRangesFromBottomUp(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	edits := []TextEdit{
		{Range: TextRange{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}}, NewText: "ALPHA"},
		{Range: TextRange{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 5}}, NewText: "GAMMA"},
	}
	got, err := ApplyTextEdits(text, edits)
	if err != nil {
		t.Fatalf("ApplyTextEdits: %v", err)
	}
	want := "ALPHA\nbeta\nGAMMA\n"
	if got != want {
		t.Fatalf("ApplyTextEdits() = %q, want %q", got, want)
	}
}

func TestApplyTextEditsUsesUTF16CharacterOffsets(t *testing.T) {
	text := "emoji 😀 old\naccent é old\n"
	edits := []TextEdit{
		{Range: TextRange{Start: Position{Line: 0, Character: 9}, End: Position{Line: 0, Character: 12}}, NewText: "new"},
		{Range: TextRange{Start: Position{Line: 1, Character: 9}, End: Position{Line: 1, Character: 12}}, NewText: "new"},
	}
	got, err := ApplyTextEdits(text, edits)
	if err != nil {
		t.Fatalf("ApplyTextEdits: %v", err)
	}
	want := "emoji 😀 new\naccent é new\n"
	if got != want {
		t.Fatalf("ApplyTextEdits() = %q, want %q", got, want)
	}
}

func TestApplyTextEditsRejectsHalfSurrogateOffset(t *testing.T) {
	_, err := ApplyTextEdits("😀\n", []TextEdit{{Range: TextRange{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 1}}, NewText: "x"}})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected half-surrogate offset error, got %v", err)
	}
}

func TestApplyTextEditsRejectsInvalidUTF8(t *testing.T) {
	_, err := ApplyTextEdits(string([]byte{0xff, '\n'}), []TextEdit{{Range: TextRange{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 1}}, NewText: "x"}})
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("expected invalid UTF-8 error, got %v", err)
	}
}

func TestApplyTextEditsRejectsInvalidRange(t *testing.T) {
	_, err := ApplyTextEdits("one\n", []TextEdit{{Range: TextRange{Start: Position{Line: 3, Character: 0}, End: Position{Line: 3, Character: 1}}, NewText: "x"}})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected outside-document error, got %v", err)
	}
}

func TestApplyTextEditsRejectsOverlappingRanges(t *testing.T) {
	_, err := ApplyTextEdits("alpha\n", []TextEdit{
		{Range: TextRange{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 3}}, NewText: "A"},
		{Range: TextRange{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 4}}, NewText: "B"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlapping edit range") {
		t.Fatalf("expected overlapping edit error, got %v", err)
	}
}

func TestPreviewHashIsStable(t *testing.T) {
	if got, want := PreviewHash("hello"), PreviewHash("hello"); got != want || got == "" {
		t.Fatalf("PreviewHash stability failed: got %q want %q", got, want)
	}
}
