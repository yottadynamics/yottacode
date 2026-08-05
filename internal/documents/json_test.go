package documents

import (
	"context"
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
}
