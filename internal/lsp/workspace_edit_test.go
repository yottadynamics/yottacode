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

func TestApplyTextEditsRejectsInvalidRange(t *testing.T) {
	_, err := ApplyTextEdits("one\n", []TextEdit{{Range: TextRange{Start: Position{Line: 3, Character: 0}, End: Position{Line: 3, Character: 1}}, NewText: "x"}})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected outside-document error, got %v", err)
	}
}
