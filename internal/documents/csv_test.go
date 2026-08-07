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
	// The warning names the window, not just the count — with offset
	// available, "the first 50" is no longer the only possible answer.
	if !hasWarningContaining(res.Warnings, "rows 1-50 of 250") {
		t.Errorf("expected a truncation warning naming the window 1-50 of 250, got %v", res.Warnings)
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

// writeCSV writes body to a temp file and returns its path. Test bodies
// use \ufeff escapes for BOMs — a literal mark in Go source is a syntax
// error, which is its own small reminder of how invisible these are.
func writeCSV(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCSVExtractorStripsUTF8BOM: Windows Excel prefixes exports with a
// BOM, and encoding/csv folds it into the first column's name — so "id"
// arrives as "<BOM>id" and every later match on that column silently
// misses.
func TestCSVExtractorStripsUTF8BOM(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "bom.csv", "\ufeffid,name\n1,Widget\n"),
	})

	if len(res.Metadata.Columns) == 0 || res.Metadata.Columns[0] != "id" {
		t.Errorf("Columns = %q, want first column %q with the BOM stripped", res.Metadata.Columns, "id")
	}
}

// TestCSVExtractorDetectsSemicolonDelimiter is the silent-wrong-answer
// regression. Excel writes ";"-delimited .csv in every European locale;
// parsing one as comma-delimited collapses each line into a single
// column and reports it as a perfectly valid one-column table.
func TestCSVExtractorDetectsSemicolonDelimiter(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "euro.csv", "id;name\n1;Widget\n2;Gadget\n"),
	})

	if len(res.Metadata.Columns) != 2 {
		t.Fatalf("Columns = %q, want 2 columns from the semicolon-delimited file", res.Metadata.Columns)
	}
	if res.Metadata.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", res.Metadata.RowCount)
	}
	// Choosing a delimiter changes how every row was read, so it must
	// be reported rather than silently applied.
	if !hasWarningContaining(res.Warnings, `detected ";"`) {
		t.Errorf("a sniffed delimiter must be reported, got %v", res.Warnings)
	}
}

func TestCSVExtractorDetectsPipeDelimiter(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "pipe.csv", "id|name\n1|Widget\n"),
	})

	if len(res.Metadata.Columns) != 2 {
		t.Errorf("Columns = %q, want 2 columns from the pipe-delimited file", res.Metadata.Columns)
	}
}

// TestCSVExtractorCommaFileIsNotWarned: sniffing must stay invisible on
// ordinary files, or every normal read grows a spurious warning.
func TestCSVExtractorCommaFileIsNotWarned(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "plain.csv", "id,name\n1,Widget\n"),
	})

	if hasWarningContaining(res.Warnings, "detected") {
		t.Errorf("a comma-delimited .csv must not produce a delimiter warning, got %v", res.Warnings)
	}
}

// TestCSVExtractorSniffIgnoresQuotedDelimiters: a header like
// `"last, first",age` has more commas inside quotes than out; counting
// them naively would pick the wrong separator.
func TestCSVExtractorSniffIgnoresQuotedDelimiters(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "quoted.csv", "\"last, first\";age\n\"Doe, J\";41\n"),
	})

	if len(res.Metadata.Columns) != 2 || res.Metadata.Columns[0] != "last, first" {
		t.Errorf("Columns = %q, want the quoted comma kept inside one field", res.Metadata.Columns)
	}
}

// TestTSVExtractorDoesNotSniff: .tsv states its separator, so a tab file
// whose data contains commas must not be re-read as comma-delimited.
func TestTSVExtractorDoesNotSniff(t *testing.T) {
	res := mustExtract(t, NewTSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "d.tsv", "id\tnote\n1\ta,b,c,d\n"),
	})

	if len(res.Metadata.Columns) != 2 {
		t.Fatalf("Columns = %q, want 2 tab-separated columns", res.Metadata.Columns)
	}
	if hasWarningContaining(res.Warnings, "detected") {
		t.Errorf(".tsv must not sniff its delimiter, got %v", res.Warnings)
	}
}

// TestCSVExtractorDetectsHeaderlessFile: treating row 1 as column names
// unconditionally both loses that record and mislabels every column.
func TestCSVExtractorDetectsHeaderlessFile(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "raw.csv", "1,Widget\n2,Gadget\n3,Doohickey\n"),
	})

	if len(res.Metadata.Columns) != 0 {
		t.Errorf("Columns = %q, want none for a headerless file", res.Metadata.Columns)
	}
	if res.Metadata.RowCount != 3 {
		t.Errorf("RowCount = %d, want 3 — the first row is data, not a header", res.Metadata.RowCount)
	}
	if !hasWarningContaining(res.Warnings, "no header row detected") {
		t.Errorf("a headerless file must say so, got %v", res.Warnings)
	}
	// Shape must still report the real width, not len(header)==0.
	if res.Metadata.Shape != "2 columns" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "2 columns")
	}
}

func TestCSVExtractorTextHeaderIsStillAHeader(t *testing.T) {
	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path: writeCSV(t, "h.csv", "id,name\n1,Widget\n"),
	})

	if len(res.Metadata.Columns) != 2 || res.Metadata.Columns[0] != "id" {
		t.Errorf("Columns = %q, want the text header preserved", res.Metadata.Columns)
	}
	if hasWarningContaining(res.Warnings, "no header row detected") {
		t.Errorf("a normal header must not trip headerless detection, got %v", res.Warnings)
	}
}

// TestCSVExtractorHasHeaderOverride covers the heuristic's known blind
// spot in both directions: a genuine header of bare years reads as data,
// and a headerless file of pure text reads as a header. has_header is
// the escape hatch for exactly these.
func TestCSVExtractorHasHeaderOverride(t *testing.T) {
	yes, no := true, false

	forced := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path:      writeCSV(t, "years.csv", "2024,2025\n10,20\n"),
		HasHeader: &yes,
	})
	if len(forced.Metadata.Columns) != 2 || forced.Metadata.Columns[0] != "2024" {
		t.Errorf("Columns = %q, want the numeric header honored under has_header=true", forced.Metadata.Columns)
	}
	if hasWarningContaining(forced.Warnings, "no header row detected") {
		t.Errorf("an explicit has_header must not emit the auto-detect warning, got %v", forced.Warnings)
	}

	suppressed := mustExtract(t, NewCSVExtractor(), ExtractRequest{
		Path:      writeCSV(t, "text.csv", "Widget,Red\nGadget,Blue\n"),
		HasHeader: &no,
	})
	if len(suppressed.Metadata.Columns) != 0 {
		t.Errorf("Columns = %q, want none under has_header=false", suppressed.Metadata.Columns)
	}
	if suppressed.Metadata.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2 under has_header=false", suppressed.Metadata.RowCount)
	}
}

// TestCSVExtractorOffsetWindow: paging is the point of offset — rows
// 3-4 of a 6-row file must come back with labels that say so.
func TestCSVExtractorOffsetWindow(t *testing.T) {
	path := writeCSV(t, "page.csv", "id,name\n1,a\n2,b\n3,c\n4,d\n5,e\n6,f\n")

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, Offset: 2, MaxRows: 2})

	if !strings.Contains(res.Sections[0].Text, "3 | c") || !strings.Contains(res.Sections[0].Text, "4 | d") {
		t.Errorf("preview = %q, want rows 3 and 4", res.Sections[0].Text)
	}
	if strings.Contains(res.Sections[0].Text, "1 | a") || strings.Contains(res.Sections[0].Text, "5 | e") {
		t.Errorf("preview = %q, must contain only the requested window", res.Sections[0].Text)
	}
	if res.Sections[0].Label != "rows 3-4" {
		t.Errorf("Label = %q, want %q — the label is what makes the unit unambiguous", res.Sections[0].Label, "rows 3-4")
	}
	// The header must survive paging; without it a later page is
	// unreadable.
	if len(res.Metadata.Columns) != 2 {
		t.Errorf("Columns = %q, want the header repeated on every page", res.Metadata.Columns)
	}
	// RowCount stays the file's true total so the caller knows how far
	// it can keep paging.
	if res.Metadata.RowCount != 6 {
		t.Errorf("RowCount = %d, want 6", res.Metadata.RowCount)
	}
}

// TestCSVExtractorPagesCoverEveryRowExactlyOnce is the property that
// makes paging trustworthy: walking the file in pages must reproduce it
// in order, with no row skipped and none repeated at a boundary.
func TestCSVExtractorPagesCoverEveryRowExactlyOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,value\n")
	for i := range 97 { // deliberately not a multiple of the page size
		fmt.Fprintf(&b, "%d,v%d\n", i, i)
	}
	path := writeCSV(t, "many.csv", b.String())

	const page = 10
	var got []string
	for offset := 0; ; offset += page {
		res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, Offset: offset, MaxRows: page})
		lines := strings.Split(res.Sections[0].Text, "\n")
		if len(lines) <= 1 { // header only == past the end
			break
		}
		got = append(got, lines[1:]...) // drop the repeated header
		if offset > 200 {
			t.Fatal("paging failed to terminate")
		}
	}

	if len(got) != 97 {
		t.Fatalf("paged %d rows, want 97 (gaps or duplicates at a page boundary)", len(got))
	}
	for i, line := range got {
		if want := fmt.Sprintf("%d | v%d", i, i); line != want {
			t.Fatalf("row %d = %q, want %q — pages are out of order or overlapping", i, line, want)
		}
	}
}

// TestCSVExtractorOffsetPastEndIsReported: an empty window otherwise
// looks exactly like an empty file, which would end a paging loop a
// page early and silently drop the tail.
func TestCSVExtractorOffsetPastEndIsReported(t *testing.T) {
	path := writeCSV(t, "short.csv", "id,name\n1,a\n2,b\n")

	res := mustExtract(t, NewCSVExtractor(), ExtractRequest{Path: path, Offset: 50})

	if !hasWarningContaining(res.Warnings, "offset 50 is past the last data row") {
		t.Errorf("an out-of-range offset must be reported, got %v", res.Warnings)
	}
}
