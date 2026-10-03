package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

// readManyTrailerSlack bounds the "files not read" trailer, which is listed on
// top of the content budget because it only echoes paths the caller supplied.
const readManyTrailerSlack = 512

// bigLines returns roughly n bytes of 80-byte lines ("xxxx…\n").
func bigLines(n int) string {
	return strings.Repeat(strings.Repeat("x", 79)+"\n", n/80)
}

func newReadMany(dir string) *ReadManyFilesTool {
	return &ReadManyFilesTool{Cwd: NewCwdRef(dir)}
}

var receiptRe = regexp.MustCompile(`# hashline path=(\S+) offset=(\d+) length=(\d+) hash=([0-9a-f]{16})`)
var anchoredLineRe = regexp.MustCompile(`(?m)^ *\d+#[0-9a-f]{8}\t`)
var anchoredPrefixRe = regexp.MustCompile(`^ *\d+#[0-9a-f]{8}\t`)

func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// assertReceiptsCoverRenderedLines is the core anchors invariant: every
// receipt must hash exactly the file bytes that were rendered as anchored
// lines in its section — no more (a hash over unseen bytes could bless an edit
// the model never saw) and no less.
func assertReceiptsCoverRenderedLines(t *testing.T, dir, out string) {
	t.Helper()
	sections := strings.Split(out, "==> ")
	checked := 0
	for _, sec := range sections {
		m := receiptRe.FindStringSubmatch(sec)
		if m == nil {
			continue
		}
		checked++
		path := m[1]
		offset, _ := strconv.Atoi(m[2])
		length, _ := strconv.Atoi(m[3])
		src, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if offset+length > len(src) {
			t.Fatalf("%s: receipt offset+length=%d exceeds file size %d", path, offset+length, len(src))
		}
		want, err := hashline.HashSpan(src, offset, length)
		if err != nil {
			t.Fatalf("%s: HashSpan: %v", path, err)
		}
		if want.Hash != m[4] {
			t.Errorf("%s: receipt hash %s does not match bytes [%d:%d] (hash %s)", path, m[4], offset, offset+length, want.Hash)
		}
		// Rebuild the covered bytes from what the section actually rendered:
		// strip the line#anchor prefixes and rejoin. This must equal the bytes
		// the receipt hashes (bar the final newline, which the layout drops).
		body := sec[strings.Index(sec, "\n")+1:]  // after the "path <==" header line
		body = body[strings.Index(body, "\n")+1:] // after the receipt line
		if i := strings.Index(body, "\n…["); i >= 0 {
			body = body[:i] // truncation marker or budget note
		}
		body = strings.TrimSuffix(body, "\n") // separator before the next section
		var rendered []string
		for line := range strings.SplitSeq(body, "\n") {
			rendered = append(rendered, anchoredPrefixRe.ReplaceAllString(line, ""))
		}
		got := strings.Join(rendered, "\n")
		covered := src[offset : offset+length]
		if want := strings.TrimSuffix(string(covered), "\n"); got != want {
			t.Errorf("%s: receipt covers %d bytes but the rendered lines reproduce %d (first difference near byte %d)",
				path, len(want), len(got), firstDiff(got, want))
		}
	}
	if checked == 0 {
		t.Fatalf("no receipts found in output: %q", out)
	}
}

// --- aggregate cap -----------------------------------------------------

func TestReadManyFilesTool_AggregateCapIsHard(t *testing.T) {
	for _, anchors := range []bool{false, true} {
		t.Run(fmt.Sprintf("anchors=%t", anchors), func(t *testing.T) {
			tmp := t.TempDir()
			for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
				writeFile(t, tmp, n, bigLines(500*1024))
			}
			out, err := newReadMany(tmp).Execute(context.Background(),
				fmt.Sprintf(`{"paths":["a.txt","b.txt","c.txt"],"anchors":%t}`, anchors))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if limit := maxReadManyTotalBytes + readManyTrailerSlack; len(out) > limit {
				t.Fatalf("output is %d bytes; advertised cap is %d (+%d trailer slack)", len(out), maxReadManyTotalBytes, readManyTrailerSlack)
			}
			if !strings.Contains(out, "==> a.txt <==") {
				t.Errorf("first file must always be read: %.200q", out)
			}
		})
	}
}

func TestReadManyFilesTool_AnchorsReceiptsMatchRenderedLinesUnderTruncation(t *testing.T) {
	tmp := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt"} {
		writeFile(t, tmp, n, bigLines(400*1024))
	}
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","b.txt"],"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertReceiptsCoverRenderedLines(t, tmp, out)
}

// --- anchors + offset --------------------------------------------------

func TestReadManyFilesTool_AnchorsWithOffsetUseAbsoluteLineNumbers(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "line1\nline2\nline3\n")
	// Byte offset 6 is the start of "line2".
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"offset":6,"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		fmt.Sprintf("%6d#%s\tline2", 2, anchorHashForLine(2, "line2")),
		fmt.Sprintf("%6d#%s\tline3", 3, anchorHashForLine(3, "line3")),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	assertReceiptsCoverRenderedLines(t, tmp, out)
}

func TestReadManyFilesTool_AnchorsMatchReadFileForSameWindow(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "one\ntwo\nthree\nfour\n")
	many, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"offset":8,"anchors":true}`) // "three\n…"
	if err != nil {
		t.Fatalf("read_many_files: %v", err)
	}
	single, err := (&ReadFileTool{Cwd: NewCwdRef(tmp)}).Execute(context.Background(), `{"path":"a.txt","offset":3,"anchors":true}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	for _, line := range anchoredLineRe.FindAllString(single, -1) {
		if !strings.Contains(many, line) {
			t.Errorf("read_many_files anchor %q (from read_file) missing: %q", line, many)
		}
	}
}

// --- truncation boundaries --------------------------------------------

func TestReadManyFilesTool_LimitTruncatesOnLineBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", strings.Repeat("abcdefghi\n", 10)) // 10-byte lines
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"limit":25}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// limit=25 falls mid-way through the third 10-byte line; the window must
	// back up to the end of the second.
	want := "==> a.txt <==\nabcdefghi\nabcdefghi\n\n…[truncated]"
	if out != want {
		t.Errorf("got %q, want two whole lines then the truncation marker %q", out, want)
	}
}

func TestReadManyFilesTool_LimitDoesNotSplitMultibyteRune(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", strings.Repeat("é", 10)) // one long line, 2 bytes per rune
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"limit":5}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Errorf("output is not valid UTF-8 (rune split): %q", out)
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("a rune-boundary cut must not be mistaken for a binary file: %q", out)
	}
	if !strings.Contains(out, "éé") || !strings.Contains(out, "[truncated]") {
		t.Errorf("want two whole runes and a truncation marker, got %q", out)
	}
}

// --- per-file failures -------------------------------------------------

func TestReadManyFilesTool_MissingFileIsInlineError(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha")
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","typo.txt"]}`)
	if err != nil {
		t.Fatalf("one missing file must not abort the batch: %v", err)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("readable file dropped: %q", out)
	}
	if !strings.Contains(out, "==> typo.txt <==") || !strings.Contains(out, "[error:") {
		t.Errorf("missing file should get an inline error section: %q", out)
	}
}

func TestReadManyFilesTool_AllFilesFailingReturnsError(t *testing.T) {
	tmp := t.TempDir()
	_, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["nope.txt"]}`)
	if err == nil || !strings.Contains(err.Error(), "nope.txt") {
		t.Fatalf("err = %v, want an error naming the failed path", err)
	}
}

func TestReadManyFilesTool_DirectoryIsInlineError(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha")
	if err := os.Mkdir(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","sub"]}`)
	if err != nil {
		t.Fatalf("directory must not abort the batch: %v", err)
	}
	if !strings.Contains(out, "==> sub <==") || !strings.Contains(out, "not a regular file") {
		t.Errorf("want inline not-a-regular-file error for a directory: %q", out)
	}
}

func TestReadManyFilesTool_FIFODoesNotHang(t *testing.T) {
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	writeFile(t, tmp, "a.txt", "alpha")
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","pipe"]}`)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("FIFO must not abort the batch: %v", r.err)
		}
		if !strings.Contains(r.out, "alpha") || !strings.Contains(r.out, "not a regular file") {
			t.Errorf("want a.txt content plus an inline not-a-regular-file error: %q", r.out)
		}
	case <-time.After(3 * time.Second):
		// Unblock the stuck open(2) so the goroutine does not outlive the test.
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("read_many_files hung opening a FIFO")
	}
}

func TestReadManyFilesTool_BinaryAndNonUTF8AreSkippedInline(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha")
	writeFile(t, tmp, "nul.dat", "ab\x00cd")
	writeFile(t, tmp, "latin1.txt", "caf\xe9\n")
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","nul.dat","latin1.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.ContainsRune(out, 0) || strings.Contains(out, "\xe9") {
		t.Errorf("binary/non-UTF-8 bytes leaked into output: %q", out)
	}
	if got := strings.Count(out, "[skipped:"); got != 2 {
		t.Errorf("want 2 skipped markers (nul.dat, latin1.txt), got %d: %q", got, out)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("text file dropped: %q", out)
	}
}

func TestReadManyFilesTool_ValidMultibyteTextIsNotSkipped(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "héllo wörld 日本語 🙂\n")
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "日本語 🙂") || strings.Contains(out, "skipped") {
		t.Errorf("valid UTF-8 was skipped or mangled: %q", out)
	}
}

func TestReadManyFilesTool_OffsetInsideRuneIsNotMistakenForBinary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "aéb\n") // é occupies bytes 1..2
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"offset":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("offset landing inside a rune must not skip the file: %q", out)
	}
}

// --- security + lifecycle ---------------------------------------------

func TestReadManyFilesTool_DeniedPathRefusesWholeCallBeforeAnyRead(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "ok.txt", "public")
	writeFile(t, tmp, "secret.key", "TOPSECRET")
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(tmp), DenyReadPaths: []string{filepath.Join(tmp, "secret.key")}}
	// A missing file listed before the denied one must not turn the denial into
	// an inline error: denial is a whole-call refusal.
	out, err := tool.Execute(context.Background(), `{"paths":["missing.txt","ok.txt","secret.key"]}`)
	if err == nil {
		t.Fatalf("denied path must refuse the whole call, got out=%q", out)
	}
	if strings.Contains(out, "public") || strings.Contains(out, "TOPSECRET") {
		t.Errorf("content leaked alongside a denied path: %q", out)
	}
}

func TestReadManyFilesTool_HonorsCancelledContext(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newReadMany(tmp).Execute(ctx, `{"paths":["a.txt"]}`); err == nil {
		t.Fatal("cancelled context must abort the call")
	}
}
