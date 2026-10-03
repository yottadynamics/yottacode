package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// Section format is consumed by the TUI, so it is locked down exactly.
func TestReadManyFilesTool_SectionFormatIsStable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "b.txt", "B")
	// "a-missing.txt" sorts first, so an error section leads the output: the
	// separator must not add a stray blank line before the first section.
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["b.txt","a-missing.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "==> a-missing.txt <==\n[error: no such file or directory]\n==> b.txt <==\nB"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestReadManyFilesTool_OffsetPastEOFIsEmptySection(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "short\n")
	for _, anchors := range []bool{false, true} {
		out, err := newReadMany(tmp).Execute(context.Background(),
			fmt.Sprintf(`{"paths":["a.txt"],"offset":1000,"anchors":%t}`, anchors))
		if err != nil {
			t.Fatalf("anchors=%t: Execute: %v", anchors, err)
		}
		if strings.Contains(out, "short") || strings.Contains(out, "error") || strings.Contains(out, "skipped") {
			t.Errorf("anchors=%t: offset past EOF should yield an empty section, got %q", anchors, out)
		}
		if anchors && !strings.Contains(out, "length=0") {
			t.Errorf("anchors: want a zero-length receipt, got %q", out)
		}
	}
}

func TestReadManyFilesTool_PlainBudgetCutEndsOnLineBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", bigLines(300*1024))
	writeFile(t, tmp, "b.txt", bigLines(400*1024)) // only ~212 KiB of budget left for this one
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt","b.txt"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_, bSection, ok := strings.Cut(out, "==> b.txt <==\n")
	if !ok || !strings.HasSuffix(bSection, readManyTruncMarker) {
		t.Fatalf("b.txt should be present and truncated by the aggregate budget; tail=%q", out[max(0, len(out)-80):])
	}
	body := strings.TrimSuffix(bSection, readManyTruncMarker)
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("budget cut left a partial trailing line")
	}
	want := strings.Repeat("x", 79)
	for i, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line != want {
			t.Fatalf("line %d is %d bytes, want whole 79-byte lines only", i, len(line))
		}
	}
}

func TestReadManyFilesTool_AnchorsOverlongLineIsCutWithMatchingReceipt(t *testing.T) {
	tmp := t.TempDir()
	// One 600 KB line of 2-byte runes: larger than both the per-file limit and
	// the whole aggregate budget, so it exercises the first-line-too-big path.
	writeFile(t, tmp, "a.txt", strings.Repeat("é", 300*1024))
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) > maxReadManyTotalBytes {
		t.Errorf("output is %d bytes, over the %d cap", len(out), maxReadManyTotalBytes)
	}
	if !utf8.ValidString(out) {
		t.Errorf("overlong line was cut mid-rune")
	}
	if !strings.HasSuffix(out, readManyTruncMarker) {
		t.Errorf("want a truncation marker")
	}
	assertReceiptsCoverRenderedLines(t, tmp, out)
}
