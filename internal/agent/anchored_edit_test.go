package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAnchoredLines(t *testing.T) {
	lines := buildAnchoredLines([]string{"alpha", "beta"}, 7)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if lines[0].LineNumber != 7 || lines[1].LineNumber != 8 {
		t.Fatalf("line numbers = %+v", lines)
	}
	if lines[0].Hash == "" || lines[1].Hash == "" {
		t.Fatalf("hashes should not be empty: %+v", lines)
	}
	if lines[0].Hash == lines[1].Hash {
		t.Fatalf("distinct lines should not share a hash: %+v", lines)
	}
}

func TestParseAnchoredRef(t *testing.T) {
	ref, err := parseAnchoredRef("42#deadbeef")
	if err != nil {
		t.Fatalf("parseAnchoredRef: %v", err)
	}
	if ref.LineNumber != 42 || ref.Hash != "deadbeef" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestResolveAnchoredRefAmbiguousBareHash(t *testing.T) {
	idx := buildAnchoredLineIndex([]string{"same", "same"})
	ambiguous := anchorHashForLine(1, "same")
	idx.ByHash[ambiguous] = append(idx.ByHash[ambiguous], anchoredLine{LineNumber: 2, Hash: ambiguous, Content: "same"})
	if _, err := resolveAnchoredRef(idx, anchoredRef{Hash: ambiguous, Raw: ambiguous}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous bare-hash error, got %v", err)
	}
}

func TestEditAnchoredToolReplaceRange(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "one\ntwo\nthree\n")
	idx := buildAnchoredLineIndex([]string{"one", "two", "three"})
	start := idx.ByLine[2]
	end := idx.ByLine[3]
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"` +
		"2#" + start.Hash + `","end_anchor":"3#` + end.Hash + `","new_text":"TWO\nTHREE"}]}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "1 anchored operation") {
		t.Fatalf("unexpected output: %q", out)
	}
	gotBytes, err := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(gotBytes)
	if got != "one\nTWO\nTHREE\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditAnchoredToolRejectsStaleAnchor(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "one\ntwo\n")
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"2#deadbeef","end_anchor":"2#deadbeef","new_text":"TWO"}]}`)
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("expected stale anchor error, got %v", err)
	}
}

func TestEditAnchoredToolRejectsOverlap(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "one\ntwo\nthree\nfour\n")
	idx := buildAnchoredLineIndex([]string{"one", "two", "three", "four"})
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[` +
		`{"op":"replace_range","start_anchor":"2#` + idx.ByLine[2].Hash + `","end_anchor":"3#` + idx.ByLine[3].Hash + `","new_text":"x"},` +
		`{"op":"delete_range","start_anchor":"3#` + idx.ByLine[3].Hash + `","end_anchor":"4#` + idx.ByLine[4].Hash + `"}` +
		`]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}
