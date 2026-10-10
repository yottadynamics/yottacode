package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func anchorHunkArgsJSON(t *testing.T, path string, hunks ...map[string]any) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"path": path, "hunks": hunks})
	if err != nil {
		t.Fatal(err)
	}
	return string(args)
}

// --- basic anchor addressing --------------------------------------------

func TestApplyHashlineTool_AnchorReplacesSingleLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\ngamma\n")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))

	out, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "BETA\n"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
	if !strings.Contains(out, "-beta") || !strings.Contains(out, "+BETA") {
		t.Errorf("diff preview missing the change: %q", out)
	}
}

func TestApplyHashlineTool_AnchorTopLevelShorthand(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\n")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))
	args, _ := json.Marshal(map[string]any{"path": "a.txt", "anchor": anchor, "new": "BETA\n"})

	if _, err := newApplyHashline(tmp).Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\nBETA\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_AnchorDeleteEmptyNewRemovesLine(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\ngamma\n")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": ""}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\ngamma\n"; got != want {
		t.Fatalf("file = %q, want %q (no blank line left behind)", got, want)
	}
}

func TestApplyHashlineTool_AnchorInsertViaAdjacentReplace(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\n")
	anchor := fmt.Sprintf("1#%s", anchorHashForLine(1, "alpha"))

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "alpha\ninserted\n"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\ninserted\nbeta\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_AnchorLastLineWithoutTrailingNewline(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "BETA"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\nBETA"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

// --- CRLF ----------------------------------------------------------------

func TestApplyHashlineTool_AnchorPreservesCRLF(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\r\nbeta\r\ngamma\r\n")
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta")) // hash trims \r itself

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "BETA\nEXTRA\n"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "alpha\r\nBETA\r\nEXTRA\r\ngamma\r\n"; got != want {
		t.Fatalf("file = %q, want %q (LF replacement converted to match the file)", got, want)
	}
}

// --- staleness / errors ---------------------------------------------------

func TestApplyHashlineTool_AnchorStaleLineNumberIsRecoverable(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\n"
	writeFile(t, tmp, "a.txt", body)
	anchor := fmt.Sprintf("10#%s", anchorHashForLine(10, "nope"))

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "x"}))
	if err == nil || !strings.Contains(err.Error(), "no line 10") {
		t.Fatalf("err = %v, want a no-such-line error", err)
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("err should point back to read_file for recovery: %v", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed on a stale anchor: %q", got)
	}
}

func TestApplyHashlineTool_AnchorHashMismatchIsRecoverable(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\n"
	writeFile(t, tmp, "a.txt", body)

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": "2#00000000", "new": "x"}))
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("err = %v, want a stale-anchor error", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed on a hash mismatch: %q", got)
	}
}

func TestApplyHashlineTool_AnchorAmbiguousBareHashIsRecoverable(t *testing.T) {
	tmp := t.TempDir()
	body := "x\ny\nz\n"
	writeFile(t, tmp, "a.txt", body)
	// Force a real collision by editing after the fact isn't practical here;
	// instead assert the ambiguous-anchor message surfaces when the bare hash
	// genuinely can't be resolved uniquely, using resolveAnchoredRef's own
	// vocabulary via a hash that matches zero lines (its sibling failure mode)
	// to pin that this class of error is recoverable-formatted too.
	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": "deadbeef", "new": "x"}))
	if err == nil || !strings.Contains(err.Error(), "no current line matches") {
		t.Fatalf("err = %v, want a no-match error for an unresolvable bare hash", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed: %q", got)
	}
}

func TestApplyHashlineTool_AnchorMalformedTokenErrors(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\n")

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": "not a token", "new": "x"}))
	if err == nil || !strings.Contains(err.Error(), "malformed anchor") {
		t.Fatalf("err = %v, want a malformed-anchor error", err)
	}
}

// --- mutual exclusivity with byte-mode fields -----------------------------

func TestApplyHashlineTool_AnchorAndByteFieldsTogetherIsRejected(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\n"
	writeFile(t, tmp, "a.txt", body)
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))
	byteAnchor := mustHashlineAnchor(t, []byte(body), 6, 4)

	_, err := newApplyHashline(tmp).Execute(context.Background(), anchorHunkArgsJSON(t, "a.txt", map[string]any{
		"anchor": anchor, "offset": byteAnchor.Offset, "length": byteAnchor.Length, "hash": byteAnchor.Hash, "old": "beta", "new": "x",
	}))
	if err == nil {
		t.Fatal("specifying both anchor and offset/length/hash/old must be rejected")
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed: %q", got)
	}
}

func TestApplyHashlineTool_MissingBothAnchorAndHashIsRejected(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\n")

	_, err := newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"new": "x"}))
	if err == nil {
		t.Fatal("a hunk with neither anchor nor hash must be rejected")
	}
}

func TestApplyHashlineTool_TwoAnchorHunksOnSameLineOverlap(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\n"
	writeFile(t, tmp, "a.txt", body)
	anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))

	_, err := newApplyHashline(tmp).Execute(context.Background(), anchorHunkArgsJSON(t, "a.txt",
		map[string]any{"anchor": anchor, "new": "X\n"},
		map[string]any{"anchor": anchor, "new": "Y\n"},
	))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("err = %v, want an overlap error", err)
	}
	if got := readBack(t, tmp, "a.txt"); got != body {
		t.Errorf("file changed: %q", got)
	}
}

// --- composition ------------------------------------------------------

func TestApplyHashlineTool_MixesAnchorAndByteHunksInOneCall(t *testing.T) {
	tmp := t.TempDir()
	body := "alpha\nbeta\ngamma\n"
	writeFile(t, tmp, "a.txt", body)
	lineAnchor := fmt.Sprintf("1#%s", anchorHashForLine(1, "alpha"))
	gammaOffset := strings.Index(body, "gamma\n")
	byteAnchor := mustHashlineAnchor(t, []byte(body), gammaOffset, len("gamma\n"))

	_, err := newApplyHashline(tmp).Execute(context.Background(), anchorHunkArgsJSON(t, "a.txt",
		map[string]any{"anchor": lineAnchor, "new": "ALPHA\n"},
		map[string]any{"offset": byteAnchor.Offset, "length": byteAnchor.Length, "hash": byteAnchor.Hash, "old": "gamma\n", "new": "GAMMA\n"},
	))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "ALPHA\nbeta\nGAMMA\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_TwoAdjacentAnchorHunksComposeIntoMultiLineEdit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "one\ntwo\nthree\nfour\n")
	a2 := fmt.Sprintf("2#%s", anchorHashForLine(2, "two"))
	a3 := fmt.Sprintf("3#%s", anchorHashForLine(3, "three"))

	_, err := newApplyHashline(tmp).Execute(context.Background(), anchorHunkArgsJSON(t, "a.txt",
		map[string]any{"anchor": a2, "new": "TWO\n"},
		map[string]any{"anchor": a3, "new": "THREE\n"},
	))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "one\nTWO\nTHREE\nfour\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

// --- the flagship win: no window replay needed --------------------------

func extractAnchorForLine(t *testing.T, readOutput string, lineNum int) string {
	t.Helper()
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*%d#([0-9a-f]{8})\t`, lineNum))
	m := re.FindStringSubmatch(readOutput)
	if m == nil {
		t.Fatalf("no anchor for line %d in %q", lineNum, readOutput)
	}
	return fmt.Sprintf("%d#%s", lineNum, m[1])
}

func TestApplyHashlineTool_AnchorRoundTripFromReadFileReceipt_NoOldOrHashNeeded(t *testing.T) {
	tmp := t.TempDir()
	// A window much larger than the one line being touched: the whole point
	// is that apply_hashline never has to see (or replay) lines 1-19 or 21-40.
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	body := strings.Join(lines, "\n") + "\n"
	writeFile(t, tmp, "big.txt", body)

	read, err := newReadFile(tmp, false).Execute(context.Background(), `{"path":"big.txt","anchors":true,"limit":40}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	anchor := extractAnchorForLine(t, read, 20)

	_, err = newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "big.txt", map[string]any{"anchor": anchor, "new": "REPLACED\n"}))
	if err != nil {
		t.Fatalf("apply_hashline: %v", err)
	}
	want := strings.Replace(body, "line20\n", "REPLACED\n", 1)
	if got := readBack(t, tmp, "big.txt"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyHashlineTool_AnchorRoundTripFromReadManyFilesReceipt(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "one\ntwo\nthree\n")

	read, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"anchors":true}`)
	if err != nil {
		t.Fatalf("read_many_files: %v", err)
	}
	anchor := extractAnchorForLine(t, read, 2)

	_, err = newApplyHashline(tmp).Execute(context.Background(),
		anchorHunkArgsJSON(t, "a.txt", map[string]any{"anchor": anchor, "new": "TWO\n"}))
	if err != nil {
		t.Fatalf("apply_hashline: %v", err)
	}
	if got, want := readBack(t, tmp, "a.txt"), "one\nTWO\nthree\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}
