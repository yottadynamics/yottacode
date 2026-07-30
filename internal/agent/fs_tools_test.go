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
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "     1\thi there" {
		t.Errorf("content = %q, want '     1\\thi there'", out)
	}
}

func TestReadFileTool_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "abs.txt")
	if err := os.WriteFile(path, []byte("abs-content"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: NewCwdRef("/unused")}
	out, err := tool.Execute(context.Background(), `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "     1\tabs-content" {
		t.Errorf("content = %q", out)
	}
}

// Multi-line read produces one prefixed line per source line, joined by
// '\n', with no trailing newline when the window reaches EOF.
func TestReadFileTool_MultilinePrefixed(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "alpha\nbeta\ngamma\n")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "     1\talpha\n     2\tbeta\n     3\tgamma"
	if out != want {
		t.Errorf("content = %q, want %q", out, want)
	}
}

func TestReadFileTool_Anchors(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "alpha\nbeta\n")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("anchored output lines = %d, want 2: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "#") || !strings.Contains(lines[0], "\talpha") {
		t.Fatalf("first anchored line malformed: %q", lines[0])
	}
	if !strings.Contains(lines[1], "#") || !strings.Contains(lines[1], "\tbeta") {
		t.Fatalf("second anchored line malformed: %q", lines[1])
	}
}

func TestReadFileTool_MissingPath(t *testing.T) {
	tool := &ReadFileTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("expected error on empty path")
	}
}

// A file that exceeds maxReadBytes is truncated and surfaces the marker
// — the underlying byte cap still exists as a defense in depth even
// though the tool now speaks lines.
func TestReadFileTool_TruncatesLargeFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.txt")
	// Build something that's > maxReadBytes AND contains many lines so the
	// line slicer doesn't terminate early on the implicit line limit.
	line := strings.Repeat("a", 1024) + "\n"
	big := strings.Repeat(line, (maxReadBytes/len(line))+10)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker in output of length %d", len(out))
	}
}

func TestReadFileTool_OffsetSelectsLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "one\ntwo\nthree\nfour\nfive\n")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":3}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "     3\tthree\n     4\tfour\n     5\tfive"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestReadFileTool_LimitTrimsTrailingLines(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "one\ntwo\nthree\nfour\nfive\n")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","limit":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "     1\tone\n     2\ttwo\n") {
		t.Errorf("got %q, want prefix '     1\\tone\\n     2\\ttwo\\n'", out)
	}
	if !strings.HasSuffix(out, "…[truncated]") {
		t.Errorf("expected '…[truncated]' suffix: %q", out)
	}
}

func TestReadFileTool_OffsetAndLimit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "one\ntwo\nthree\nfour\nfive\n")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":2,"limit":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "     2\ttwo\n     3\tthree\n…[truncated]"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestReadFileTool_OffsetPastEOFReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "only-line")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
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
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","limit":1000}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "     1\tshort" {
		t.Errorf("got %q, want '     1\\tshort'", out)
	}
	if strings.Contains(out, "[truncated]") {
		t.Errorf("file fits in limit, should not be truncated: %q", out)
	}
}

// offset < 1 is clamped to line 1 (the first valid 1-indexed line),
// matching how the previous byte-based shape clamped negative offsets to
// 0.
func TestReadFileTool_NegativeOffsetClampedToFirstLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "f.txt", "alpha\nbeta")
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"f.txt","offset":-7}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "     1\talpha\n     2\tbeta"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestWriteFileTool_CreatesFileAndParents(t *testing.T) {
	tmp := t.TempDir()
	tool := &WriteFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
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
	tool := &WriteFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","content":"new"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", string(got))
	}
}

func TestWriteFileTool_BadJSON(t *testing.T) {
	tool := &WriteFileTool{Cwd: NewCwdRef(t.TempDir())}
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
