package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditAnchoredToolPreservesCRLF(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	body := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := buildAnchoredLineIndex([]string{"one", "two", "three"})
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"2#` + idx.ByLine[2].Hash + `","end_anchor":"2#` + idx.ByLine[2].Hash + `","new_text":"TWO"}]}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\r\nTWO\r\nthree\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditAnchoredToolPreservesUnterminatedFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	body := "one\ntwo"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := buildAnchoredLineIndex([]string{"one", "two"})
	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"x.txt","operations":[{"op":"replace_range","start_anchor":"2#` + idx.ByLine[2].Hash + `","end_anchor":"2#` + idx.ByLine[2].Hash + `","new_text":"TWO"}]}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO" || strings.HasSuffix(string(got), "\n") {
		t.Fatalf("content = %q", got)
	}
}
