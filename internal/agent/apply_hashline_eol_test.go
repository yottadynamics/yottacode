package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newApplyHashline(dir string) *ApplyHashlineTool {
	return &ApplyHashlineTool{Cwd: NewCwdRef(dir), WriteOpts: WritePathOptions{Cwd: NewCwdRef(dir)}}
}

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(got)
}

type eolHunk struct {
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	Hash   string `json:"hash"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// hunkFor builds a hunk whose receipt covers old exactly as it appears in body,
// but whose old text is what the model sends (which may differ in line endings).
func hunkFor(t *testing.T, body, spanInFile, sentOld, sentNew string) eolHunk {
	t.Helper()
	off := strings.Index(body, spanInFile)
	if off < 0 {
		t.Fatalf("span %q not in body", spanInFile)
	}
	a := mustHashlineAnchor(t, []byte(body), off, len(spanInFile))
	return eolHunk{Offset: off, Length: len(spanInFile), Hash: a.Hash, Old: sentOld, New: sentNew}
}

func execHunks(t *testing.T, dir, name string, hunks ...eolHunk) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"path": name, "hunks": hunks})
	if err != nil {
		t.Fatal(err)
	}
	return newApplyHashline(dir).Execute(context.Background(), string(args))
}

// --- CRLF --------------------------------------------------------------

// A model reading a CRLF file emits LF: it cannot reliably reproduce invisible
// carriage returns. The receipt hashes the real (CRLF) bytes.
func TestApplyHashlineTool_CRLFFileAcceptsLFOld(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\r\nbeta\r\ngamma\r\n"
	writeFile(t, tmp, "crlf.txt", body)
	h := hunkFor(t, body, body, "alpha\nbeta\ngamma\n", "ALPHA\nbeta\ngamma\n")
	if _, err := execHunks(t, tmp, "crlf.txt", h); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "crlf.txt"), "ALPHA\r\nbeta\r\ngamma\r\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_CRLFFileKeepsEndingsConsistentForLFNew(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\r\nbeta\r\ngamma\r\n"
	writeFile(t, tmp, "crlf.txt", body)
	// Old is exact (with CRLF); new arrives with LF and adds a line.
	h := hunkFor(t, body, "beta\r\n", "beta\r\n", "BETA\nEXTRA\n")
	if _, err := execHunks(t, tmp, "crlf.txt", h); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "crlf.txt"), "alpha\r\nBETA\r\nEXTRA\r\ngamma\r\n"; got != want {
		t.Errorf("file = %q, want %q (no mixed line endings)", got, want)
	}
}

func TestApplyHashlineTool_CRLFFileSingleLineEditKeepsEndings(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\r\nbeta\r\ngamma\r\n"
	writeFile(t, tmp, "crlf.txt", body)
	// Mid-line span: old has no newline at all, but new brings one.
	h := hunkFor(t, body, "beta", "beta", "beta\nadded")
	if _, err := execHunks(t, tmp, "crlf.txt", h); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "crlf.txt"), "alpha\r\nbeta\r\nadded\r\ngamma\r\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_LFFileIsLeftAlone(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\n"
	writeFile(t, tmp, "lf.txt", body)
	// The model chose CRLF on purpose in an LF file; do not second-guess it.
	h := hunkFor(t, body, "beta\n", "beta\n", "BETA\r\n")
	if _, err := execHunks(t, tmp, "lf.txt", h); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "lf.txt"), "alpha\nBETA\r\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_MixedEndingsExactOldIsHonoured(t *testing.T) {
	tmp := t.TempDir()
	body := "a\r\nb\nc\r\n" // two CRLF, one LF: CRLF-dominant but genuinely mixed
	writeFile(t, tmp, "mixed.txt", body)
	// The model reproduced the exact mixed bytes, so it is managing endings
	// itself: neither old nor new may be rewritten.
	h := hunkFor(t, body, body, body, "A\r\nb\nc\r\n")
	if _, err := execHunks(t, tmp, "mixed.txt", h); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "mixed.txt"), "A\r\nb\nc\r\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_EOLAlignmentDoesNotMaskARealMismatch(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\r\nbeta\r\n"
	writeFile(t, tmp, "crlf.txt", body)
	h := hunkFor(t, body, body, "alpha\nDIFFERENT\n", "x") // hash is for the real bytes
	_, err := execHunks(t, tmp, "crlf.txt", h)
	if err == nil || !strings.Contains(err.Error(), "do not match anchor hash") {
		t.Fatalf("err = %v, want a hash mismatch", err)
	}
	if got := readBack(t, tmp, "crlf.txt"); got != body {
		t.Errorf("file changed on mismatch: %q", got)
	}
}

// --- validation surfaced through the tool ----------------------------

func TestApplyHashlineTool_RejectsLengthThatDisagreesWithOld(t *testing.T) {
	tmp := t.TempDir()
	body := "aaa\nfoo\nbbb\nccc\n"
	writeFile(t, tmp, "a.txt", body)
	h := hunkFor(t, body, "foo", "foo", "X")
	h.Length = 8 // would replace "foo\nbbb\n" if trusted
	if _, err := execHunks(t, tmp, "a.txt", h); err == nil {
		t.Fatal("a length that disagrees with old must be rejected")
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed: %q", got)
	}
}

func TestApplyHashlineTool_EmptyOldExplainsHowToInsert(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "one\ntwo\n")
	h := eolHunk{Offset: 4, Length: 0, Hash: "e3b0c44298fc1c14", Old: "", New: "inserted\n"}
	_, err := execHunks(t, tmp, "a.txt", h)
	if err == nil || !strings.Contains(err.Error(), "adjacent") {
		t.Fatalf("err = %v, want guidance to anchor on adjacent text", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != "one\ntwo\n" {
		t.Errorf("file changed: %q", got)
	}
}

func TestApplyHashlineTool_MultiHunkPreviewShowsEveryChange(t *testing.T) {
	tmp := t.TempDir()
	body := numberedLines(200, "l")
	writeFile(t, tmp, "f.txt", body)
	first := hunkFor(t, body, "l1\n", "l1\n", "L1\n")
	last := hunkFor(t, body, "l200\n", "l200\n", "L200\n")
	out, err := execHunks(t, tmp, "f.txt", first, last)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"-l1\n+L1\n", "-l200\n+L200\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview is missing %q:\n%s", want, out)
		}
	}
	if bytes.Contains([]byte(out), []byte("l100")) {
		t.Errorf("unchanged lines leaked into the preview:\n%s", out)
	}
}
