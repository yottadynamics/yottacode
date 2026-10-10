package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Anchors read before an earlier edit shifted lines must still resolve when
// the anchored text is unique nearby.
func TestEditAnchoredToolRelocatesShiftedAnchor(t *testing.T) {
	tmp := t.TempDir()
	old := buildAnchoredLineIndex([]string{"one", "two", "three"})
	staleRef := "3#" + old.ByLine[3].Hash
	// Two lines were inserted above "three" after the read.
	writeFile(t, tmp, "x.txt", "one\nnew1\nnew2\ntwo\nthree\n")
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"` + staleRef + `","end_anchor":"` + staleRef + `","new_text":"THREE"}]}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("shifted anchor should relocate: %v", err)
	}
	if !strings.Contains(out, "relocated") || !strings.Contains(out, "3→5") {
		t.Errorf("result should report the relocation, got %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if string(got) != "one\nnew1\nnew2\ntwo\nTHREE\n" {
		t.Fatalf("content = %q", got)
	}
}

// A repeated line must never relocate: two candidates means ambiguity.
func TestEditAnchoredToolDoesNotRelocateAmbiguousAnchor(t *testing.T) {
	tmp := t.TempDir()
	old := buildAnchoredLineIndex([]string{"a", "}", "b"})
	staleRef := "2#" + old.ByLine[2].Hash
	body := "a\nx\n}\ny\n}\nb\n"
	writeFile(t, tmp, "x.txt", body)
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[{"op":"delete_range","start_anchor":"` + staleRef + `","end_anchor":"` + staleRef + `"}]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("expected stale anchor error, got %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if string(got) != body {
		t.Fatalf("file changed: %q", got)
	}
}

// The stale error must carry the current text and fresh anchors so the caller
// can retry without another read.
func TestEditAnchoredToolStaleErrorIncludesCurrentContext(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "one\nchanged line\nthree\n")
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"2#deadbeef","end_anchor":"2#deadbeef","new_text":"x"}]}`)
	if err == nil {
		t.Fatal("expected error")
	}
	idx := buildAnchoredLineIndex([]string{"one", "changed line", "three"})
	for _, want := range []string{"stale anchor", "changed line", "2#" + idx.ByLine[2].Hash, "3#" + idx.ByLine[3].Hash} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}
