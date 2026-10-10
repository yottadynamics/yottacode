package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// An anchor read before lines were inserted above it must still apply, and the
// result must say it moved.
func TestApplyHashlineTool_RelocatesShiftedAnchor(t *testing.T) {
	tmp := t.TempDir()
	staleAnchor := fmt.Sprintf("3#%s", anchorHashForLine(3, "three"))
	writeFile(t, tmp, "a.txt", "one\nnew1\nnew2\ntwo\nthree\n")

	out, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": staleAnchor, "new": "THREE\n"}))
	if err != nil {
		t.Fatalf("shifted anchor should relocate: %v", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != "one\nnew1\nnew2\ntwo\nTHREE\n" {
		t.Errorf("file = %q", got)
	}
	if !strings.Contains(out, "relocated") || !strings.Contains(out, "3→5") {
		t.Errorf("result should report the relocation, got %q", out)
	}
}

// A repeated line has two candidates; relocation must refuse and leave the
// file untouched.
func TestApplyHashlineTool_DoesNotRelocateAmbiguousAnchor(t *testing.T) {
	tmp := t.TempDir()
	body := "a\nx\n}\ny\n}\nb\n"
	writeFile(t, tmp, "a.txt", body)
	staleAnchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "}"))

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": staleAnchor, "new": "\n"}))
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("err = %v, want a stale-anchor error", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed: %q", got)
	}
}

// An exact anchor must not produce a relocation note.
func TestApplyHashlineTool_ExactAnchorHasNoRelocationNote(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\n")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))

	out, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "BETA\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "relocated") {
		t.Errorf("unexpected relocation note: %q", out)
	}
}
