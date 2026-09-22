package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

func newReadFile(dir string, images bool) *ReadFileTool {
	return &ReadFileTool{Cwd: NewCwdRef(dir), SupportsImages: images}
}

// execOrFailOnHang runs fn and fails the test if it does not return promptly.
// On a hang it opens the FIFO's write end so the stuck open(2) is released and
// the goroutine does not outlive the test.
func execOrFailOnHang(t *testing.T, fifo string, fn func() (string, error)) (string, error) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := fn()
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(3 * time.Second):
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("read_file hung on a FIFO")
		return "", nil
	}
}

// --- non-regular files -------------------------------------------------

func TestReadFileTool_FIFODoesNotHang(t *testing.T) {
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	_, err := execOrFailOnHang(t, fifo, func() (string, error) {
		return newReadFile(tmp, false).Execute(context.Background(), `{"path":"pipe"}`)
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file error", err)
	}
}

func TestReadFileTool_ImageNamedFIFODoesNotHang(t *testing.T) {
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "pipe.png")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// SupportsImages=true is the path that reads the file body.
	_, err := execOrFailOnHang(t, fifo, func() (string, error) {
		res, err := newReadFile(tmp, true).ExecuteMultimodal(context.Background(), `{"path":"pipe.png"}`)
		return res.Content, err
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file error", err)
	}
}

func TestReadFileTool_DirectoryIsNotARegularFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := newReadFile(tmp, false).Execute(context.Background(), `{"path":"sub"}`)
	if err == nil || !strings.Contains(err.Error(), "not a regular file (directory)") {
		t.Fatalf("err = %v, want a not-a-regular-file (directory) error", err)
	}
}

func TestReadFileTool_MissingFileStillReportsNotFound(t *testing.T) {
	_, err := newReadFile(t.TempDir(), false).Execute(context.Background(), `{"path":"nope.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("err = %v, want the underlying not-found error preserved", err)
	}
}

// --- binary / non-UTF-8 -----------------------------------------------

func TestReadFileTool_BinaryAndNonUTF8AreSkipped(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"nul.dat", "ab\x00cd\n", "[skipped: not UTF-8 text (contains NUL bytes)]"},
		{"latin1.txt", "caf\xe9\n", "[skipped: not UTF-8 text (invalid UTF-8)]"},
	}
	for _, c := range cases {
		for _, anchors := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/anchors=%t", c.name, anchors), func(t *testing.T) {
				tmp := t.TempDir()
				writeFile(t, tmp, c.name, c.content)
				out, err := newReadFile(tmp, false).Execute(context.Background(),
					fmt.Sprintf(`{"path":%q,"anchors":%t}`, c.name, anchors))
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				// Exactly the marker: no line numbers, no receipt, no raw bytes.
				if out != c.want {
					t.Errorf("got %q, want %q", out, c.want)
				}
			})
		}
	}
}

func TestReadFileTool_ValidMultibyteTextIsNotSkipped(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "héllo wörld 日本語 🙂\n")
	for _, anchors := range []bool{false, true} {
		out, err := newReadFile(tmp, false).Execute(context.Background(),
			fmt.Sprintf(`{"path":"a.txt","anchors":%t}`, anchors))
		if err != nil {
			t.Fatalf("anchors=%t: Execute: %v", anchors, err)
		}
		if !strings.Contains(out, "日本語 🙂") || strings.Contains(out, "skipped") {
			t.Errorf("anchors=%t: valid UTF-8 skipped or mangled: %q", anchors, out)
		}
	}
}

// --- overlong line -----------------------------------------------------

func TestReadFileTool_OverlongLineIsCutOnRuneBoundary(t *testing.T) {
	tmp := t.TempDir()
	// One ~900 KB line of 3-byte runes. maxReadBytes (524288) is not a
	// multiple of 3, so a raw byte cut lands two bytes into a rune.
	body := strings.Repeat("日", 300000)
	writeFile(t, tmp, "a.txt", body)
	out, err := newReadFile(tmp, false).Execute(context.Background(), `{"path":"a.txt","anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "skipped") {
		t.Fatalf("a rune-boundary cut must not read as non-text: %.120q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("overlong line was cut mid-rune")
	}
	if !strings.HasSuffix(out, "\n…[truncated]") {
		t.Errorf("want a truncation marker, got tail %q", out[max(0, len(out)-40):])
	}
	m := receiptRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no receipt in output: %.120q", out)
	}
	length, _ := strconv.Atoi(m[3])
	if length%3 != 0 {
		t.Errorf("receipt length %d is not a whole number of 3-byte runes", length)
	}
	want, err := hashline.HashSpan([]byte(body), 0, length)
	if err != nil {
		t.Fatal(err)
	}
	if want.Hash != m[4] {
		t.Errorf("receipt hash %s does not match the first %d bytes of the file (%s)", m[4], length, want.Hash)
	}
	if !strings.Contains(out, strings.Repeat("日", length/3)) {
		t.Errorf("receipt covers %d bytes that were not all rendered", length)
	}
}
