package agent

import (
	"context"
	"fmt"
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

// bigLogLines builds a file whose total size comfortably exceeds
// maxReadBytes, so any line-window logic that only ever looks at a
// fixed prefix will be caught.
func bigLogLines(t *testing.T, n int) (dir, name string) {
	t.Helper()
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "2026-08-06 line %06d some message\n", i)
	}
	dir = t.TempDir()
	writeFile(t, dir, "big.log", b.String())
	return dir, "big.log"
}

// TestReadFileTool_OffsetReachesPastByteCap is the regression: the
// reader used to take a fixed maxReadBytes prefix and index offset into
// *that*, so every line beyond the prefix was unreachable — and asking
// for one returned an empty string, indistinguishable from a genuine
// end-of-file. On a 30k-line log that silently capped reads at roughly
// line 14,000.
func TestReadFileTool_OffsetReachesPastByteCap(t *testing.T) {
	dir, name := bigLogLines(t, 30000)
	tool := &ReadFileTool{Cwd: NewCwdRef(dir)}

	for _, offset := range []int{20000, 29999, 30000} {
		out, err := tool.Execute(context.Background(),
			fmt.Sprintf(`{"path":%q,"offset":%d,"limit":1}`, name, offset))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		want := fmt.Sprintf("line %06d", offset-1)
		if !strings.Contains(out, want) {
			t.Errorf("offset %d returned %q, want it to contain %q", offset, out, want)
		}
	}
}

// TestReadFileTool_OffsetPastLastLineStillEmpty: now that deep offsets
// are reachable, an empty result must mean only one thing — genuinely
// past the end.
func TestReadFileTool_OffsetPastLastLineStillEmpty(t *testing.T) {
	dir, name := bigLogLines(t, 30000)
	tool := &ReadFileTool{Cwd: NewCwdRef(dir)}

	for _, offset := range []int{30001, 50000} {
		out, err := tool.Execute(context.Background(),
			fmt.Sprintf(`{"path":%q,"offset":%d,"limit":1}`, name, offset))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if out != "" {
			t.Errorf("offset %d returned %q, want empty (past the last line)", offset, out)
		}
	}
}

// TestReadFileTool_PagesCoverEveryLineExactlyOnce walks a file larger
// than the byte cap in windows and checks the windows reassemble it in
// order — the property that catches an off-by-one at a page boundary,
// which would corrupt a read without ever erroring.
func TestReadFileTool_PagesCoverEveryLineExactlyOnce(t *testing.T) {
	const total = 30000
	dir, name := bigLogLines(t, total)
	tool := &ReadFileTool{Cwd: NewCwdRef(dir)}

	const page = 4000
	seen := 0
	for offset := 1; ; offset += page {
		out, err := tool.Execute(context.Background(),
			fmt.Sprintf(`{"path":%q,"offset":%d,"limit":%d}`, name, offset, page))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if out == "" {
			break
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(out, "…[truncated]"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			want := fmt.Sprintf("line %06d", seen)
			if !strings.Contains(line, want) {
				t.Fatalf("line %d = %q, want it to contain %q (pages overlap or skip)", seen, line, want)
			}
			seen++
		}
		if offset > total+page {
			t.Fatal("paging failed to terminate")
		}
	}

	if seen != total {
		t.Errorf("paged %d lines, want %d", seen, total)
	}
}

// TestReadFileTool_SkipsPastOverlongLine: skipping is done with a
// bounded reader loop, so a single multi-megabyte line before the
// window must not be buffered whole just to get past it.
func TestReadFileTool_SkipsPastOverlongLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "huge.txt", "a\n"+strings.Repeat("x", 5_000_000)+"\nTARGET\n")

	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"huge.txt","offset":3,"limit":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "     3\tTARGET" {
		t.Errorf("got %q, want the line after the overlong one", out)
	}
}

// TestReadFileTool_WindowStillCappedByBytes: reachability changed, the
// budget did not — a single window must still not return more than
// maxReadBytes of content.
func TestReadFileTool_WindowStillCappedByBytes(t *testing.T) {
	dir, name := bigLogLines(t, 30000)
	tool := &ReadFileTool{Cwd: NewCwdRef(dir)}

	out, err := tool.Execute(context.Background(),
		fmt.Sprintf(`{"path":%q,"offset":15000,"limit":30000}`, name))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Line-number prefixes add ~7 bytes/line, so allow generous slack
	// over the raw content budget while still catching "no cap at all".
	if len(out) > 2*maxReadBytes {
		t.Errorf("window returned %d bytes, want it bounded near maxReadBytes=%d", len(out), maxReadBytes)
	}
	if !strings.HasSuffix(out, "…[truncated]") {
		t.Error("a byte-capped window must still report truncation")
	}
}
