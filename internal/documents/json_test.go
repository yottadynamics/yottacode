package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONExtractorMatch(t *testing.T) {
	e := &JSONExtractor{}
	if !e.Match("data.json") || !e.Match("data.jsonl") {
		t.Errorf("JSONExtractor must match both .json and .jsonl")
	}
	if e.Match("data.csv") {
		t.Errorf("JSONExtractor must not match .csv")
	}
}

func TestJSONExtractorObjectShape(t *testing.T) {
	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "object.json")})

	if res.Metadata.Kind != "json" {
		t.Errorf("Kind = %q, want json", res.Metadata.Kind)
	}
	if !strings.Contains(res.Metadata.Shape, "3 top-level key") {
		t.Errorf("Shape = %q, want it to mention 3 top-level keys", res.Metadata.Shape)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings for a small well-formed file: %v", res.Warnings)
	}
}

func TestJSONExtractorArrayShape(t *testing.T) {
	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "array.json")})

	if !strings.Contains(res.Metadata.Shape, "array of 3 element") {
		t.Errorf("Shape = %q, want it to mention array of 3 elements", res.Metadata.Shape)
	}
}

func TestJSONExtractorInvalidJSON(t *testing.T) {
	_, err := (&JSONExtractor{}).Extract(context.Background(), ExtractRequest{Path: filepath.Join("testdata", "invalid.json")})
	if err == nil {
		t.Fatal("expected an error for invalid JSON, got nil")
	}
}

func TestJSONExtractorOversizeFallsBackToRawPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	content := `{"a": "` + strings.Repeat("x", 500) + `"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxBytes: 50})

	if res.Metadata.Shape != "unknown (exceeds byte cap)" {
		t.Errorf("Shape = %q, want the byte-cap fallback shape", res.Metadata.Shape)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a byte-cap warning")
	}
}

// TestJSONExtractorExactBoundaryStillParses is the regression for the
// stale-os.Stat bug: a file whose size lands exactly at MaxBytes must
// still parse normally (no fallback, no warning) — the truncation
// check must key off whether the read actually needed more than
// MaxBytes bytes, not a separately-stat'd size that could disagree
// with what was actually read.
func TestJSONExtractorExactBoundaryStillParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.json")
	content := `{"a":1}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxBytes: int64(len(content))})

	if res.Metadata.Shape == "unknown (exceeds byte cap)" {
		t.Error("file size equals MaxBytes exactly; should parse normally, not fall back to a raw preview")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings at the exact boundary, got %v", res.Warnings)
	}
}

func TestJSONLExtractorSamplesWithLineProvenance(t *testing.T) {
	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "sample.jsonl")})

	if res.Metadata.Kind != "jsonl" {
		t.Errorf("Kind = %q, want jsonl", res.Metadata.Kind)
	}
	// 4 non-blank lines total, but line 3 is malformed and skipped.
	if res.Metadata.RowCount != 4 {
		t.Errorf("RowCount = %d, want 4 (total lines seen, malformed included)", res.Metadata.RowCount)
	}
	if len(res.Sections) != 3 {
		t.Fatalf("got %d sections, want 3 (malformed line 3 skipped)", len(res.Sections))
	}
	if res.Sections[0].Label != "JSONL line 1" {
		t.Errorf("first section label = %q, want %q", res.Sections[0].Label, "JSONL line 1")
	}
	// The record after the malformed one must still report its true
	// source line number (4), not a compacted index (3).
	if res.Sections[2].Label != "JSONL line 4" {
		t.Errorf("third section label = %q, want %q", res.Sections[2].Label, "JSONL line 4")
	}
	foundMalformedWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "malformed") {
			foundMalformedWarning = true
		}
	}
	if !foundMalformedWarning {
		t.Errorf("expected a malformed-line warning, got %v", res.Warnings)
	}
}

func TestJSONLExtractorMaxRowsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	var b strings.Builder
	for range 250 {
		b.WriteString(`{"i": 1}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxRows: 10})

	if res.Metadata.RowCount != 250 {
		t.Errorf("RowCount = %d, want 250", res.Metadata.RowCount)
	}
	if len(res.Sections) != 10 {
		t.Errorf("got %d sections, want 10 (MaxRows cap)", len(res.Sections))
	}
	// The shortfall warning must name which cap caused it, the way the
	// CSV extractor does — "showing 10 of 250" alone leaves the reader
	// guessing whether to raise max_rows or max_chars.
	if !hasWarningContaining(res.Warnings, "10-record sample cap") {
		t.Errorf("truncation warning must name the row cap, got %v", res.Warnings)
	}
}

// TestJSONLExtractorCharCapNamesItsReason is the silent-truncation
// regression: a single oversized first record trips the character
// budget and stops sampling, which used to render as a bare
// "showing 0 of N records" with no cause at all — the exact failure
// mode this package exists to prevent.
func TestJSONLExtractorCharCapNamesItsReason(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.jsonl")
	wide := `{"blob": "` + strings.Repeat("x", 500) + `"}`
	if err := os.WriteFile(path, []byte(wide+"\n"+wide+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxChars: 50})

	if len(res.Sections) != 0 {
		t.Fatalf("got %d sections, want 0 (every record exceeds the char cap)", len(res.Sections))
	}
	if !hasWarningContaining(res.Warnings, "50-character preview cap") {
		t.Errorf("char-capped truncation must name the char cap, got %v", res.Warnings)
	}
	if !hasWarningContaining(res.Warnings, "showing 0 of 2 records") {
		t.Errorf("truncation warning must state the counts, got %v", res.Warnings)
	}
}

// TestJSONExtractorStripsUTF8BOM: a BOM is not skippable whitespace to
// encoding/json — it fails the whole parse with "invalid character 'ï'",
// making a BOM-prefixed export completely unreadable rather than merely
// degraded.
func TestJSONExtractorStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.json")
	if err := os.WriteFile(path, []byte("\ufeff{\"a\": 1}"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path})

	if !strings.Contains(res.Metadata.Shape, "a") {
		t.Errorf("Shape = %q, want the object's key set", res.Metadata.Shape)
	}
}

// TestJSONLExtractorStripsUTF8BOM: in JSONL the BOM only breaks the
// first line, which the extractor then counts as malformed and skips —
// so record 1 vanishes from the sample with nothing but a generic
// "1 malformed line(s)" to hint at why.
func TestJSONLExtractorStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.jsonl")
	if err := os.WriteFile(path, []byte("\ufeff{\"a\": 1}\n{\"a\": 2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path})

	if len(res.Sections) != 2 {
		t.Fatalf("got %d sections, want 2 — the BOM must not cost record 1", len(res.Sections))
	}
	if hasWarningContaining(res.Warnings, "malformed") {
		t.Errorf("a stripped BOM must not be reported as a malformed line, got %v", res.Warnings)
	}
}

func hasWarningContaining(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestJSONLExtractorOffsetWindow: record labels are absolute source line
// numbers, so a paged window must still cite the real lines — that is
// what lets the model point a user at an exact line in the file.
func TestJSONLExtractorOffsetWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.jsonl")
	var b strings.Builder
	for i := range 10 {
		fmt.Fprintf(&b, `{"i": %d}`+"\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, Offset: 4, MaxRows: 3})

	if len(res.Sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(res.Sections))
	}
	if res.Sections[0].Label != "JSONL line 5" {
		t.Errorf("first label = %q, want %q — labels stay absolute across pages", res.Sections[0].Label, "JSONL line 5")
	}
	if !strings.Contains(res.Sections[0].Text, `"i": 4`) {
		t.Errorf("first section = %q, want record index 4", res.Sections[0].Text)
	}
	if !hasWarningContaining(res.Warnings, "showing records 5-7 of 10") {
		t.Errorf("warning must name the window, got %v", res.Warnings)
	}
}

func TestJSONLExtractorOffsetPastEndIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"a\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, Offset: 99})

	if !hasWarningContaining(res.Warnings, "offset 99 is past the last record") {
		t.Errorf("an out-of-range offset must be reported, got %v", res.Warnings)
	}
}

// TestJSONExtractorOffsetIsCharacterBased: a single JSON document has no
// rows, so its window is measured in characters of the pretty-printed
// form — the same unit the preview is already capped in.
func TestJSONExtractorOffsetIsCharacterBased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.json")
	if err := os.WriteFile(path, []byte(`{"alpha":1,"bravo":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	first := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxChars: 10})
	second := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, Offset: 10, MaxChars: 10})

	if second.Sections[0].Text == first.Sections[0].Text {
		t.Error("an offset window must return different content than the first page")
	}
	if !strings.HasPrefix(second.Sections[0].Label, "document characters 11-") {
		t.Errorf("Label = %q, want it to name the character window", second.Sections[0].Label)
	}
	// The shape summary describes the whole document, so it must not
	// change just because a later page was requested.
	if second.Metadata.Shape != first.Metadata.Shape {
		t.Errorf("Shape changed across pages: %q vs %q", first.Metadata.Shape, second.Metadata.Shape)
	}
}

// TestJSONExtractorByteCappedPathHonorsOffset: the byte-cap fallback
// returns a raw preview instead of a parsed one, and it used to render
// that preview without applying Offset — so paging a large .json handed
// back page 1 forever, a loop that never terminates and never errors.
func TestJSONExtractorByteCappedPathHonorsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	if err := os.WriteFile(path, []byte(`{"a":"`+strings.Repeat("x", 500)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// MaxBytes below the file size forces the raw-preview fallback.
	req := ExtractRequest{Path: path, MaxBytes: 50, MaxChars: 10}
	first := mustExtract(t, &JSONExtractor{}, req)

	req.Offset = 20
	second := mustExtract(t, &JSONExtractor{}, req)

	if first.Metadata.Shape != "unknown (exceeds byte cap)" {
		t.Fatalf("Shape = %q, want the byte-cap fallback (the test must exercise that path)", first.Metadata.Shape)
	}
	if second.Sections[0].Text == first.Sections[0].Text {
		t.Error("the byte-capped raw preview ignored offset and returned page 1 again")
	}
	if !strings.Contains(second.Sections[0].Label, "characters 21-") {
		t.Errorf("Label = %q, want it to name the character window", second.Sections[0].Label)
	}
}

// TestJSONExtractorOffsetPastEndLabelsEmptyWindow: an offset past the
// end must not render a backwards range like "characters 51-10".
func TestJSONExtractorOffsetPastEndLabelsEmptyWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, Offset: 500})

	if res.Sections[0].Text != "" {
		t.Errorf("Text = %q, want empty for a window past the end", res.Sections[0].Text)
	}
	if !strings.Contains(res.Sections[0].Label, "offset is past the end") {
		t.Errorf("Label = %q, want it to say the window is empty", res.Sections[0].Label)
	}
	if !hasWarningContaining(res.Warnings, "past the end of the document") {
		t.Errorf("expected an out-of-range warning, got %v", res.Warnings)
	}
}

// TestJSONExtractorRecognizesJSONLContent: a .json file holding one
// value per line is a common mislabelled export. Read as a single
// document it doesn't degrade — encoding/json errors out on the second
// value, so the whole file comes back unusable.
func TestJSONExtractorRecognizesJSONLContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actually.json")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path})

	if res.Metadata.Kind != "jsonl" {
		t.Errorf("Kind = %q, want jsonl", res.Metadata.Kind)
	}
	if len(res.Sections) != 3 {
		t.Errorf("got %d sections, want 3", len(res.Sections))
	}
	// Reinterpreting the file is exactly the kind of decision that must
	// be visible rather than inferred from the output shape.
	if !hasWarningContaining(res.Warnings, "parsed as JSONL") {
		t.Errorf("the reinterpretation must be reported, got %v", res.Warnings)
	}
}

// TestJSONExtractorKeepsInvalidJSONError: the JSONL retry must not
// swallow a genuine syntax error behind a vaguer "no records" result —
// "invalid character 'n'" is the more useful answer for a broken file.
func TestJSONExtractorKeepsInvalidJSONError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := (&JSONExtractor{}).Extract(context.Background(), ExtractRequest{Path: path})
	if err == nil {
		t.Fatal("expected an error for genuinely malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("err = %v, want the original JSON parse error", err)
	}
}

// TestJSONExtractorSingleDocumentIsNotRetried: a well-formed one-line
// document must never be re-read as JSONL — the retry is strictly a
// recovery path.
func TestJSONExtractorSingleDocumentIsNotRetried(t *testing.T) {
	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "object.json")})

	if res.Metadata.Kind != "json" {
		t.Errorf("Kind = %q, want json", res.Metadata.Kind)
	}
	if hasWarningContaining(res.Warnings, "parsed as JSONL") {
		t.Errorf("a valid document must not be reinterpreted, got %v", res.Warnings)
	}
}

// TestJSONLExtractorSkipsOversizedRecordAndContinues: one fat record
// used to end sampling outright, so every later record was dropped even
// when dozens would have fit. Labels are absolute line numbers, which is
// what makes the resulting gap readable rather than misleading.
func TestJSONLExtractorSkipsOversizedRecordAndContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")

	var b strings.Builder
	b.WriteString(`{"small":1}` + "\n")
	b.WriteString(`{"blob":"` + strings.Repeat("x", 400) + `"}` + "\n")
	for i := range 5 {
		fmt.Fprintf(&b, `{"small":%d}`+"\n", i+2)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &JSONExtractor{}, ExtractRequest{Path: path, MaxChars: 100})

	if len(res.Sections) != 6 {
		t.Fatalf("got %d sections, want 6 — the oversized record must not suppress the rest", len(res.Sections))
	}
	if res.Sections[0].Label != "JSONL line 1" || res.Sections[1].Label != "JSONL line 3" {
		t.Errorf("labels = %q/%q, want line 1 then line 3 (the skip must be visible)",
			res.Sections[0].Label, res.Sections[1].Label)
	}
	if !hasWarningContaining(res.Warnings, "1 record(s) too large to include") {
		t.Errorf("the skipped record must be counted in the warning, got %v", res.Warnings)
	}
}
