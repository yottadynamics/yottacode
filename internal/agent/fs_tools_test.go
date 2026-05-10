package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(path, []byte("hi there"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "hi there" {
		t.Errorf("content = %q, want 'hi there'", out)
	}
}

func TestReadFileTool_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "abs.txt")
	if err := os.WriteFile(path, []byte("abs-content"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: "/unused"}
	out, err := tool.Execute(context.Background(), `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "abs-content" {
		t.Errorf("content = %q", out)
	}
}

func TestReadFileTool_BadJSON(t *testing.T) {
	tool := &ReadFileTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `{not json`); err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}

func TestReadFileTool_MissingPath(t *testing.T) {
	tool := &ReadFileTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error on empty path")
	}
}

func TestReadFileTool_TruncatesLargeFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.txt")
	big := strings.Repeat("a", maxReadBytes+1000)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker in output of length %d", len(out))
	}
}

func TestReadFileTool_Offset(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "0123456789")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":4}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "456789" {
		t.Errorf("got %q, want '456789'", out)
	}
}

func TestReadFileTool_Limit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "abcdefghij")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","limit":3}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "abc") {
		t.Errorf("got %q, want prefix 'abc'", out)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker since file is 10 bytes and limit is 3: %q", out)
	}
}

func TestReadFileTool_OffsetAndLimit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "0123456789")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":3,"limit":4}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "3456") {
		t.Errorf("got %q, want prefix '3456'", out)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker (more bytes remain): %q", out)
	}
}

func TestReadFileTool_OffsetPastEOFReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "tiny")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":1000}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "" {
		t.Errorf("got %q, want empty", out)
	}
}

func TestReadFileTool_LimitBeyondFileNoTruncation(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "short")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","limit":1000}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "short" {
		t.Errorf("got %q, want 'short'", out)
	}
	if strings.Contains(out, "[truncated]") {
		t.Errorf("file fits in limit, should not be truncated: %q", out)
	}
}

func TestReadFileTool_NegativeOffsetClampedToZero(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "alpha")
	tool := &ReadFileTool{Cwd: tmp}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":-7}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "alpha" {
		t.Errorf("got %q, want 'alpha'", out)
	}
}

func TestWriteFileTool_CreatesFileAndParents(t *testing.T) {
	tmp := t.TempDir()
	tool := &WriteFileTool{Cwd: tmp, WriteOpts: WritePathOptions{Cwd: tmp}}
	out, err := tool.Execute(context.Background(), `{"path":"nested/dir/out.txt","content":"payload"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "wrote") {
		t.Errorf("confirmation missing: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "nested", "dir", "out.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("written content = %q, want 'payload'", string(got))
	}
}

func TestWriteFileTool_Overwrites(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &WriteFileTool{Cwd: tmp, WriteOpts: WritePathOptions{Cwd: tmp}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","content":"new"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", string(got))
	}
}

func TestWriteFileTool_BadJSON(t *testing.T) {
	tool := &WriteFileTool{Cwd: t.TempDir()}
	if _, err := tool.Execute(context.Background(), `garbage`); err == nil {
		t.Errorf("expected error on bad JSON")
	}
}

// write_file's preview is a single line — the invocation summary only.
// The content body lands in the unified tool-card body via argsJSON, not
// inlined in the preview; otherwise a multi-line preview breaks the card
// header layout (duration tag misaligns) and gets duplicated when the
// permissions auto-allow notice fires.
func TestWriteFileTool_PreviewIsSingleLineSummary(t *testing.T) {
	tool := &WriteFileTool{}
	argJSON := `{"path":"a.txt","content":"` + strings.Repeat("x", 1000) + `"}`
	preview := tool.PreviewCall(argJSON)
	if preview != "write_file(a.txt, 1000 bytes)" {
		t.Errorf("preview = %q, want %q", preview, "write_file(a.txt, 1000 bytes)")
	}
	if strings.Contains(preview, "\n") {
		t.Errorf("preview must be single-line: %q", preview)
	}
}
