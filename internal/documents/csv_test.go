package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustExtract(t *testing.T, e Extractor, req ExtractRequest) ExtractResult {
	t.Helper()
	res, err := e.Extract(context.Background(), req)
	if err != nil {
		t.Fatalf("Extract(%s) returned error: %v", req.Path, err)
	}
	return res
}

func TestCSVExtractorMatch(t *testing.T) {
	csvE := NewCSVExtractor()
	tsvE := NewTSVExtractor()

	if !csvE.Match("data.csv") {
		t.Errorf("CSVExtractor.Match(%q) = false, want true", "data.csv")
	}
	if !csvE.Match("data.CSV") {
		t.Errorf("CSVExtractor.Match must be case-insensitive on the extension")
	}
	if csvE.Match("data.tsv") {
		t.Errorf("CSVExtractor.Match must reject .tsv")
	}
	if !tsvE.Match("data.tsv") || tsvE.Match("data.csv") {
		t.Errorf("TSVExtractor.Match must accept only .tsv")
	}
}

func TestCSVExtractorBasic(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: filepath.Join("testdata", "basic.csv")})

	if res.Metadata.Kind != "csv" {
		t.Errorf("Kind = %q, want csv", res.Metadata.Kind)
	}
	wantHeader := []string{"id", "name", "amount"}
	if strings.Join(res.Metadata.Columns, ",") != strings.Join(wantHeader, ",") {
		t.Errorf("Columns = %v, want %v", res.Metadata.Columns, wantHeader)
	}
	if res.Metadata.RowCount != 3 {
		t.Errorf("RowCount = %d, want 3", res.Metadata.RowCount)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings for a small well-formed file: %v", res.Warnings)
	}
	if len(res.Sections) != 1 || !strings.Contains(res.Sections[0].Text, "Widget") {
		t.Errorf("Sections missing expected content: %+v", res.Sections)
	}
}

func TestTSVExtractorBasic(t *testing.T) {
	res := mustExtract(t, NewTSVExtractor(), ExtractRequest{Path: filepath.Join("testdata", "basic.tsv")})

	if res.Metadata.Kind != "tsv" {
		t.Errorf("Kind = %q, want tsv", res.Metadata.Kind)
	}
	if res.Metadata.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", res.Metadata.RowCount)
	}
}

// TestCSVExtractorEmbeddedNewline is the correctness fix this extractor
// exists for: a quoted field containing a literal newline must parse as
// one logical row, not shear into a bogus extra row the way a naive
// line-split would.
func TestCSVExtractorEmbeddedNewline(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: filepath.Join("testdata", "quoted_newline.csv")})

	if res.Metadata.RowCount != 2 {
		t.Fatalf("RowCount = %d, want 2 (embedded newline must not create a phantom row)", res.Metadata.RowCount)
	}
	if !strings.Contains(res.Sections[0].Text, "first line\nsecond line") {
		t.Errorf("embedded newline was not preserved intact in the extracted text: %q", res.Sections[0].Text)
	}
}

func TestCSVExtractorMalformedRowStopsCleanly(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: filepath.Join("testdata", "malformed.csv")})

	if res.Metadata.RowCount != 1 {
		t.Fatalf("RowCount = %d, want 1 (only the row before the unterminated quote)", res.Metadata.RowCount)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "stopped parsing") {
		t.Errorf("expected a stopped-parsing warning, got %v", res.Warnings)
	}
}

func TestCSVExtractorEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path})

	if res.Metadata.RowCount != 0 || len(res.Metadata.Columns) != 0 {
		t.Errorf("empty file should report zero rows and no columns, got %+v", res.Metadata)
	}
	if res.Sections[0].Label != "no data rows" {
		t.Errorf("Label = %q, want %q", res.Sections[0].Label, "no data rows")
	}
}

func TestCSVExtractorMaxRowsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.csv")
	var b strings.Builder
	b.WriteString("id,value\n")
	for range 250 {
		b.WriteString("x,y\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxRows: 50})

	if res.Metadata.RowCount != 250 {
		t.Errorf("RowCount = %d, want 250 (the true count, even though the preview is capped)", res.Metadata.RowCount)
	}
	if got := strings.Count(res.Sections[0].Text, "\n") + 1; got != 51 { // header + 50 sampled rows
		t.Errorf("preview has %d lines, want 51 (header + MaxRows)", got)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "first 50 of 250") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a truncation warning naming 50 of 250, got %v", res.Warnings)
	}
}

func TestCSVExtractorMaxBytesCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.csv")
	var b strings.Builder
	b.WriteString("id,value\n")
	for range 1000 {
		b.WriteString("0123456789,0123456789\n")
	}
	content := b.String()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxBytes: 200, MaxRows: 1000})

	if res.Metadata.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d (the true file size, not the capped read)", res.Metadata.SizeBytes, len(content))
	}
	if res.Metadata.RowCount >= 1000 {
		t.Errorf("RowCount = %d, expected the byte cap to stop the read well before EOF", res.Metadata.RowCount)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "200-byte cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a byte-cap warning, got %v", res.Warnings)
	}
}

// TestCSVExtractorMaxBytesCap_ExactBoundaryNoWarning is the flip side of
// the byte-cap regression: a file whose size lands exactly at MaxBytes
// has nothing truncated, so no byte-cap warning should fire. This is
// the case the old info.Size()-based check could get wrong in the other
// direction if the file changed between stat and read; the
// countingReader-based check must get the boundary itself right too.
func TestCSVExtractorMaxBytesCap_ExactBoundaryNoWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.csv")
	content := "id,value\n1,2\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxBytes: int64(len(content))})

	for _, w := range res.Warnings {
		if strings.Contains(w, "byte cap") {
			t.Errorf("file size equals MaxBytes exactly; should not warn about a byte cap, got %v", res.Warnings)
		}
	}
}

// TestCSVExtractorMaxCharsTruncation_WideRowsKeepRowCountHonest is the
// regression for the char-budget-only truncation bug: rows narrow
// enough to all fit under MaxRows, but wide enough that MaxChars runs
// out first. Metadata.RowCount, the section label, and a warning must
// all reflect what's ACTUALLY in the text, not the full sampled count.
func TestCSVExtractorMaxCharsTruncation_WideRowsKeepRowCountHonest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.csv")
	var b strings.Builder
	b.WriteString("id,value\n")
	wideValue := strings.Repeat("x", 500)
	for range 100 {
		b.WriteString("1," + wideValue + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// 100 rows of ~500 chars each vastly exceeds a 2000-char budget,
	// while MaxRows (200) is never reached — the char budget must be
	// what actually stops the preview.
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxChars: 2000})

	rowsInText := strings.Count(res.Sections[0].Text, "\n") // header doesn't count itself
	if res.Metadata.RowCount != 100 {
		t.Fatalf("RowCount = %d, want 100 (the true count)", res.Metadata.RowCount)
	}
	wantLabel := fmt.Sprintf("rows 1-%d", rowsInText)
	if res.Sections[0].Label != wantLabel {
		t.Errorf("Label = %q, want %q (must match what's actually in Text, not the full 100)", res.Sections[0].Label, wantLabel)
	}
	if rowsInText >= 100 {
		t.Fatalf("expected the char budget to stop well short of all 100 rows, got %d", rowsInText)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "character preview cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a character-preview-cap warning when the char budget (not the row cap) truncates, got %v", res.Warnings)
	}
}

// TestCSVExtractorOversizedFirstRow_RuneSafeAndWarned regression-tests
// both halves of the header-truncation bug: the cut must land on a
// UTF-8 rune boundary (not split a multi-byte character), and it must
// be reported as a warning rather than silently shortening the row.
func TestCSVExtractorOversizedFirstRow_RuneSafeAndWarned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversized_header.csv")
	// A multi-byte rune (é, 2 bytes in UTF-8) placed so it straddles a
	// small MaxChars cut point.
	header := strings.Repeat("a", 9) + "é" + strings.Repeat("b", 10)
	content := header + "\n1,2\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxChars: 10})

	text := res.Sections[0].Text
	if !utf8.ValidString(strings.TrimSuffix(text, "…[truncated]")) {
		t.Errorf("truncated header is not valid UTF-8: %q", text)
	}
	if res.Metadata.RowCount != 1 || res.Sections[0].Label != "no data rows" {
		t.Errorf("expected the data row to be excluded (header alone exhausted the budget): RowCount=%d Label=%q", res.Metadata.RowCount, res.Sections[0].Label)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "character preview cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a character-preview-cap warning when the header alone is truncated, got %v", res.Warnings)
	}
}

// TestCSVExtractorHeaderOnlyFile_TruncatedHeaderStillWarns is the
// regression for the case the above test's fixture accidentally missed:
// a file with NO data rows at all (rowCount == 0), where the header
// alone gets truncated. rowsWritten (0) and rowCount (0) are equal, so
// gating the warning on "rowsWritten < rowCount" alone would stay
// silent even though the header text was genuinely cut.
func TestCSVExtractorHeaderOnlyFile_TruncatedHeaderStillWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "header_only.csv")
	content := strings.Repeat("a", 78) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxChars: 10})

	if res.Metadata.RowCount != 0 {
		t.Fatalf("RowCount = %d, want 0 (no data rows in this fixture)", res.Metadata.RowCount)
	}
	if !strings.HasSuffix(res.Sections[0].Text, "…[truncated]") {
		t.Fatalf("expected the header text itself to show truncation, got %q", res.Sections[0].Text)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "character preview cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("truncated header with zero data rows must still warn (rowsWritten==rowCount==0 must not suppress it), got %v", res.Warnings)
	}
}

// TestCSVExtractorByteCap_DoesNotLeakContentPastTheCap is the
// regression for the "+1 lookahead" side effect: reading one byte past
// the cap to detect truncation must never let that extra byte's
// content reach the parser. Here the file's 5th byte closes a quoted
// field that the 4-byte cap should leave open — if the extra byte
// leaked through, encoding/csv would see a complete, well-formed row
// instead of the malformed one the cap is supposed to produce.
func TestCSVExtractorByteCap_DoesNotLeakContentPastTheCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_at_boundary.csv")
	// "h\n\"ab\"\n" -- header "h", then a quoted data row `"ab"`. The
	// closing quote sits at byte index 5 (0-indexed); capping at 5
	// bytes must leave the quote unterminated.
	content := "h\n\"ab\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, MaxBytes: 5})

	if res.Metadata.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0 (the quoted field must be left unterminated by the cap, not completed by a leaked byte)", res.Metadata.RowCount)
	}
	foundStopped, foundByteCap := false, false
	for _, w := range res.Warnings {
		if strings.Contains(w, "stopped parsing") {
			foundStopped = true
		}
		if strings.Contains(w, "byte cap") {
			foundByteCap = true
		}
	}
	if !foundStopped {
		t.Errorf("expected a stopped-parsing warning (unterminated quote), got %v", res.Warnings)
	}
	if !foundByteCap {
		t.Errorf("expected a byte-cap warning, got %v", res.Warnings)
	}
}
