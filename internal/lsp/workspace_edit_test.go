package lsp

import (
	"encoding/json"
	"errors"
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

func TestWorkspaceEditNormalizesChangesAndDocumentChanges(t *testing.T) {
	raw := json.RawMessage(`{
		"changes":{"file:///tmp/a.go":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"A"}]},
		"documentChanges":[{"textDocument":{"uri":"file:///tmp/b.go","version":1},"edits":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}},"newText":"B"}]}]
	}`)
	edit, err := workspaceEdit(raw)
	if err != nil {
		t.Fatalf("workspaceEdit: %v", err)
	}
	if len(edit.Edits) != 2 {
		t.Fatalf("edit count = %d, want 2: %+v", len(edit.Edits), edit.Edits)
	}
	if edit.Edits[0].Path != "/tmp/a.go" || edit.Edits[1].Path != "/tmp/b.go" {
		t.Fatalf("unexpected paths: %+v", edit.Edits)
	}
}

func TestWorkspaceEditRejectsResourceOperations(t *testing.T) {
	for _, kind := range []string{"create", "rename", "delete"} {
		t.Run(kind, func(t *testing.T) {
			_, err := workspaceEdit(json.RawMessage(`{"documentChanges":[{"kind":"` + kind + `","uri":"file:///tmp/a.go"}]}`))
			if !errors.Is(err, ErrUnsupportedWorkspaceEdit) || !strings.Contains(err.Error(), "file "+kind) {
				t.Fatalf("expected unsupported %s operation, got %v", kind, err)
			}
		})
	}
}

func TestWorkspaceEditRejectsUnsupportedDocumentChangeShape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"missing text document", `{"documentChanges":[{"edits":[]}]}`},
		{"non text edit", `{"documentChanges":[{"textDocument":{"uri":"file:///tmp/a.go"},"edits":[{"newText":"x"}]}]}`},
		{"annotation", `{"documentChanges":[{"textDocument":{"uri":"file:///tmp/a.go"},"annotationId":"1","edits":[]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workspaceEdit(json.RawMessage(tc.raw))
			if !errors.Is(err, ErrUnsupportedWorkspaceEdit) {
				t.Fatalf("expected unsupported workspace edit, got %v", err)
			}
		})
	}
}

func TestApplyTextEditsHandlesCRLF(t *testing.T) {
	got, err := ApplyTextEdits("one\r\ntwo\r\n", []TextEdit{{Range: TextRange{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 3}}, NewText: "TWO"}})
	if err != nil || got != "one\r\nTWO\r\n" {
		t.Fatalf("ApplyTextEdits CRLF = %q, %v", got, err)
	}
}

func TestPreviewHashIsStable(t *testing.T) {
	if got, want := PreviewHash("hello"), PreviewHash("hello"); got != want || got == "" {
		t.Fatalf("PreviewHash stability failed: got %q want %q", got, want)
	}
}
