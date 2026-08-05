package documents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXMLExtractorMatch(t *testing.T) {
	e := &XMLExtractor{}
	if !e.Match("data.xml") || !e.Match("data.XML") {
		t.Errorf("XMLExtractor must match .xml case-insensitively")
	}
	if e.Match("data.json") {
		t.Errorf("XMLExtractor must not match .json")
	}
}

func TestXMLExtractorBasic(t *testing.T) {
	res := mustExtract(t, &XMLExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "catalog.xml")})

	if res.Metadata.Kind != "xml" {
		t.Errorf("Kind = %q, want xml", res.Metadata.Kind)
	}
	if !strings.Contains(res.Metadata.Shape, "root <catalog>") {
		t.Errorf("Shape = %q, want it to mention root <catalog>", res.Metadata.Shape)
	}
	if !strings.Contains(res.Metadata.Shape, "item(2)") {
		t.Errorf("Shape = %q, want it to count item(2)", res.Metadata.Shape)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings for a small well-formed file: %v", res.Warnings)
	}
	if !strings.Contains(res.Sections[0].Text, "Widget") || !strings.Contains(res.Sections[0].Text, "9.99") {
		t.Errorf("extracted text missing expected content: %q", res.Sections[0].Text)
	}
}

// TestXMLExtractorMalformedReturnsPartialResult checks the "here's what
// we got" behavior: a decode error partway through must not fail the
// whole call, and must not discard the root element and text captured
// before the error.
func TestXMLExtractorMalformedReturnsPartialResult(t *testing.T) {
	res := mustExtract(t, &XMLExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "malformed.xml")})

	if !strings.Contains(res.Metadata.Shape, "root <catalog>") {
		t.Errorf("Shape = %q, want the root captured before the parse error", res.Metadata.Shape)
	}
	if !strings.Contains(res.Sections[0].Text, "Widget") {
		t.Errorf("expected text captured before the parse error, got %q", res.Sections[0].Text)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "stopped parsing") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a stopped-parsing warning, got %v", res.Warnings)
	}
}

func TestXMLExtractorEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.xml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res := mustExtract(t, &XMLExtractor{}, ExtractRequest{Path: path})

	if res.Metadata.Shape != "empty document" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "empty document")
	}
}

func TestXMLExtractorMaxCharsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.xml")
	var b strings.Builder
	b.WriteString("<doc>")
	for range 50 {
		b.WriteString("<p>some repeated sentence content here.</p>")
	}
	b.WriteString("</doc>")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &XMLExtractor{}, ExtractRequest{Path: path, MaxChars: 100})

	if len(res.Sections[0].Text) > 100+len("…[truncated]") {
		t.Errorf("preview text longer than expected after MaxChars cap: %d bytes", len(res.Sections[0].Text))
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "characters of text content") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a character-truncation warning, got %v", res.Warnings)
	}
}
