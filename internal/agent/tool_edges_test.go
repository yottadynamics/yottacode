package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- capped writer (used by grep + git output) ----------------------------

func TestCapped_WritesUnderLimit(t *testing.T) {
	var buf bytes.Buffer
	w := &capped{buf: &buf, max: 100}
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("returned n = %d, want 5", n)
	}
	if buf.String() != "hello" {
		t.Errorf("buf = %q", buf.String())
	}
}

func TestCapped_TruncatesAtBoundary(t *testing.T) {
	var buf bytes.Buffer
	w := &capped{buf: &buf, max: 5}
	n, err := w.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("hello world") {
		t.Errorf("returned n should match input len; got %d", n)
	}
	if buf.String() != "hello" {
		t.Errorf("buf should hold first 5 bytes; got %q", buf.String())
	}
}

func TestCapped_DropsAfterFull(t *testing.T) {
	var buf bytes.Buffer
	w := &capped{buf: &buf, max: 3}
	w.Write([]byte("abc"))    // fills
	n, err := w.Write([]byte("def")) // entirely dropped
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 3 {
		t.Errorf("dropped Write n = %d, want 3 (caller should still see len)", n)
	}
	if buf.String() != "abc" {
		t.Errorf("buf = %q, want 'abc'", buf.String())
	}
}

// --- read_file edge cases -------------------------------------------------

func TestReadFile_DirectoryReturnsError(t *testing.T) {
	tmp := t.TempDir()
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"."}`); err == nil {
		t.Errorf("read_file on a directory should error")
	}
}

func TestReadFile_PermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission test is meaningless")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(path, []byte("locked"), 0o000); err != nil {
		t.Fatalf("setup: %v", err)
	}
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"secret.txt"}`); err == nil {
		t.Errorf("expected permission-denied error")
	}
}

// --- write_file edge cases ------------------------------------------------

func TestWriteFile_RejectsEmptyPath(t *testing.T) {
	tool := &WriteFileTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"path":"","content":"x"}`); err == nil {
		t.Errorf("write_file with empty path should error")
	}
}

func TestWriteFile_PreviewIsBytesOnly(t *testing.T) {
	tool := &WriteFileTool{Cwd: NewCwdRef(t.TempDir())}
	// Use a sentinel that doesn't collide with the literal preview shape
	// `write_file(x, NNNN bytes)`. `@` isn't in any of those tokens.
	long := strings.Repeat("@", 1000)
	preview := tool.PreviewCall(`{"path":"x","content":"` + long + `"}`)
	if !strings.Contains(preview, "1000 bytes") {
		t.Errorf("preview missing byte count: %q", preview)
	}
	if strings.ContainsRune(preview, '@') {
		t.Errorf("preview must not embed content body: %q", preview)
	}
}

// --- edit_file edge cases -------------------------------------------------

func TestEditFile_PreviewWithReplaceAllAnnotation(t *testing.T) {
	tool := &EditFileTool{Cwd: NewCwdRef(t.TempDir())}
	preview := tool.PreviewCall(`{"path":"x","old_string":"a","new_string":"b","replace_all":true}`)
	if !strings.Contains(preview, "all") {
		t.Errorf("preview should annotate replace_all mode: %q", preview)
	}
}

func TestEditFile_PreviewBadJSONIsHarmless(t *testing.T) {
	tool := &EditFileTool{Cwd: NewCwdRef(t.TempDir())}
	preview := tool.PreviewCall(`{not json`)
	// Doesn't panic; produces *some* preview string.
	if preview == "" {
		t.Errorf("preview must not be empty even on invalid JSON")
	}
}

// --- list_dir edge cases --------------------------------------------------

func TestListDir_DefaultsToCwdWhenPathOmitted(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "x")
	tool := &ListDirTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("default-to-cwd listing missing entries: %q", out)
	}
}

func TestListDir_DefaultMaxEntries(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 3; i++ {
		writeFile(t, tmp, string(rune('a'+i))+".txt", "x")
	}
	tool := &ListDirTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"a.txt", "b.txt", "c.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("entry %s missing under default max_entries: %q", want, out)
		}
	}
}

// --- glob edge cases ------------------------------------------------------

func TestGlob_RespectsMaxResults(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, tmp, "f"+string(rune('0'+i))+".go", "")
	}
	tool := &GlobTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"pattern":"*.go","max_results":3}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker with max_results=3 over 10 files: %q", out)
	}
}

// --- grep edge cases ------------------------------------------------------

func TestGrep_RegexAndIgnoreCaseTogether(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "x.txt", "Hello World\nhello there\n")
	tool := &GrepTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(),
		`{"pattern":"^h.+lo","path":".","regex":true,"ignore_case":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Both lines start with [Hh]ello; with ignore_case + regex anchor, both match.
	if !strings.Contains(out, "Hello World") {
		t.Errorf("regex+ignore_case missed 'Hello World': %q", out)
	}
}

// --- git edge cases -------------------------------------------------------

func TestGit_EmptyArgsArrayIsAnError(t *testing.T) {
	tool := &GitTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"args":[]}`); err == nil {
		t.Errorf("empty args should error")
	}
}

func TestGit_PreviewMultipleDestructiveFlags(t *testing.T) {
	tool := &GitTool{}
	preview := tool.PreviewCall(`{"args":["push","--force","--prune"]}`)
	if !strings.Contains(preview, "--force") || !strings.Contains(preview, "--prune") {
		t.Errorf("preview should annotate every destructive flag: %q", preview)
	}
}

func TestGit_RequiresApprovalForBadJSON(t *testing.T) {
	// Defensive default: malformed args → require approval, never silently
	// auto-execute something we can't classify.
	tool := &GitTool{}
	if !tool.RequiresApproval(`{not json`) {
		t.Errorf("malformed JSON must default to requiring approval")
	}
}
