package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	// Tests frequently create nested repo paths; mirror real file-tool behavior
	// by materializing parent directories before writing the fixture file.
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	return p
}

func TestEditFile_ReplacesUniqueMatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "hello.txt", "alpha beta gamma")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"hello.txt","old_string":"beta","new_string":"BETA"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "1 replacement") {
		t.Errorf("output = %q, want '1 replacement'", out)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "hello.txt"))
	if string(got) != "alpha BETA gamma" {
		t.Errorf("file = %q, want 'alpha BETA gamma'", string(got))
	}
}

func TestEditFile_RejectsNonUniqueWithoutReplaceAll(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "foo foo foo")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":"foo","new_string":"bar"}`)
	if err == nil {
		t.Fatalf("expected error for non-unique old_string")
	}
	if !strings.Contains(err.Error(), "appears 3 times") {
		t.Errorf("error = %v, want count in message", err)
	}
	// File must be untouched.
	got, _ := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if string(got) != "foo foo foo" {
		t.Errorf("file mutated despite error: %q", string(got))
	}
}

func TestEditFile_ReplaceAllRewritesEveryOccurrence(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "foo foo foo")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":"foo","new_string":"bar","replace_all":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "3 replacement") {
		t.Errorf("output = %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if string(got) != "bar bar bar" {
		t.Errorf("file = %q", string(got))
	}
}

func TestEditFile_DeletesWhenNewStringEmpty(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "keep DELETE_ME keep")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":" DELETE_ME","new_string":""}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "x.txt"))
	if string(got) != "keep keep" {
		t.Errorf("file = %q, want 'keep keep'", string(got))
	}
}

func TestEditFile_RejectsEmptyOldString(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "anything")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":"","new_string":"foo"}`); err == nil {
		t.Errorf("expected error on empty old_string")
	}
}

func TestEditFile_RejectsIdenticalOldAndNew(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "foo")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":"foo","new_string":"foo"}`); err == nil {
		t.Errorf("expected error on identical old/new")
	}
}

func TestEditFile_ErrorsWhenOldStringMissing(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "alpha")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"x.txt","old_string":"missing","new_string":"X"}`)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want 'not found'", err)
	}
}

func TestEditFile_BadJSON(t *testing.T) {
	tool := &EditFileTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{not json`); err == nil {
		t.Errorf("expected error on bad JSON")
	}
}

func TestEditFile_FileMissing(t *testing.T) {
	tool := &EditFileTool{Cwd: NewCwdRef(t.TempDir())}
	_, err := tool.Execute(context.Background(), `{"path":"nope.txt","old_string":"x","new_string":"y"}`)
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestEditFile_TolerantTrailingWhitespace(t *testing.T) {
	tmp := t.TempDir()
	// "alpha" carries trailing spaces on disk; the model's old_string omits
	// them, so the exact match misses and the tolerant matcher must catch it.
	writeFile(t, tmp, "doc.md", "alpha  \nbeta\ngamma\n")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"doc.md","old_string":"alpha\nbeta","new_string":"ALPHA\nBETA"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "ignoring trailing whitespace") {
		t.Errorf("output = %q, want tolerant-match note", out)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "doc.md"))
	if string(got) != "ALPHA\nBETA\ngamma\n" {
		t.Errorf("file = %q, want 'ALPHA\\nBETA\\ngamma\\n'", string(got))
	}
}

func TestEditFile_TolerantCRLF(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.md", "foo\r\nbar\r\n")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	out, err := tool.Execute(context.Background(), `{"path":"doc.md","old_string":"foo\nbar","new_string":"FOO\nBAR"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "1 replacement") {
		t.Errorf("output = %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "doc.md"))
	if string(got) != "FOO\nBAR\n" {
		t.Errorf("file = %q, want 'FOO\\nBAR\\n'", string(got))
	}
}

func TestEditFile_TolerantMatchRefusedWhenAmbiguous(t *testing.T) {
	tmp := t.TempDir()
	// Both blocks match "a\nb" once trailing whitespace is ignored, so the
	// tolerant matcher must refuse rather than guess a location.
	writeFile(t, tmp, "doc.md", "a  \nb\nx\na\t\nb\n")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"doc.md","old_string":"a\nb","new_string":"Z"}`)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous refusal", err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "doc.md"))
	if string(got) != "a  \nb\nx\na\t\nb\n" {
		t.Errorf("file mutated despite ambiguous refusal: %q", string(got))
	}
}

func TestEditFile_NotFoundErrorPointsAtClosestLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.md", "the quick brown fox\n")
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	_, err := tool.Execute(context.Background(), `{"path":"doc.md","old_string":"the quick brown FOX jumped","new_string":"X"}`)
	if err == nil || !strings.Contains(err.Error(), "closest line") {
		t.Fatalf("err = %v, want closest-line hint", err)
	}
}
