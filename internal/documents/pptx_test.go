package documents

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePptxFixture builds a minimal, hand-rolled .pptx: a zip archive
// with one ppt/slides/slideN.xml entry per element of slideTexts.
// PptxExtractor only ever reads those entries, so a real pptx's many
// other parts (presentation.xml, layouts, masters, _rels) aren't needed.
func writePptxFixture(t *testing.T, dir string, slideTexts []string) string {
	t.Helper()
	path := filepath.Join(dir, "deck.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for i, text := range slideTexts {
		w, err := zw.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i+1))
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		xmlDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
			`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
			`<p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>` +
			`</p:sld>`
		if _, err := w.Write([]byte(xmlDoc)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return path
}

// writePptxTableFixture builds a single-slide .pptx whose slide has a
// text shape (bodyText) followed by a bare DrawingML <a:tbl> built from
// rows. The parser tracks table structure purely by local element name
// ("tbl"/"tr"/"tc"/"t"), so the table doesn't need the full real
// <p:graphicFrame><a:graphic><a:graphicData> wrapper a real pptx has
// around it — only the <a:tbl> itself matters for what's being tested.
func writePptxTableFixture(t *testing.T, dir, bodyText string, rows [][]string) string {
	t.Helper()
	path := filepath.Join(dir, "deck.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	var tbl strings.Builder
	tbl.WriteString(`<a:tbl>`)
	for _, row := range rows {
		tbl.WriteString(`<a:tr>`)
		for _, cell := range row {
			tbl.WriteString(`<a:tc><a:txBody><a:p><a:r><a:t>` + cell + `</a:t></a:r></a:p></a:txBody></a:tc>`)
		}
		tbl.WriteString(`</a:tr>`)
	}
	tbl.WriteString(`</a:tbl>`)
	xmlDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + bodyText + `</a:t></a:r></a:p></p:txBody></p:sp>` +
		tbl.String() +
		`</p:spTree></p:cSld></p:sld>`
	if _, err := w.Write([]byte(xmlDoc)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return path
}

func TestPptxExtractorTableSectionRendered(t *testing.T) {
	dir := t.TempDir()
	path := writePptxTableFixture(t, dir, "Slide intro text", [][]string{
		{"Name", "Qty"},
		{"Widget", "3"},
	})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var tableSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "slide 1 table 1" {
			tableSec = &res.Sections[i]
		}
	}
	if tableSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "slide 1 table 1", res.Sections)
	}
	want := "Name | Qty\nWidget | 3"
	if tableSec.Text != want {
		t.Errorf("table section text = %q, want %q", tableSec.Text, want)
	}
}

// TestPptxExtractorTableTextStillInPlainSlideSection confirms the
// additive contract: a table's cell text still appears in the regular
// "slide N" section too (run together with the rest, same fidelity as
// before), not removed now that it also has a clean table section —
// mirroring PDF's own "duplicate at lower fidelity" precedent.
func TestPptxExtractorTableTextStillInPlainSlideSection(t *testing.T) {
	dir := t.TempDir()
	path := writePptxTableFixture(t, dir, "Slide intro text", [][]string{
		{"Name", "Qty"},
		{"Widget", "3"},
	})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var plainSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "slide 1" {
			plainSec = &res.Sections[i]
		}
	}
	if plainSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "slide 1", res.Sections)
	}
	for _, want := range []string{"Slide intro text", "Name", "Qty", "Widget", "3"} {
		if !strings.Contains(plainSec.Text, want) {
			t.Errorf("plain slide text missing %q: %q", want, plainSec.Text)
		}
	}
}

func TestPptxExtractorEmptyTableSkipped(t *testing.T) {
	dir := t.TempDir()
	path := writePptxTableFixture(t, dir, "No real table here", [][]string{{"", ""}, {"  ", ""}})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "table") {
			t.Errorf("expected an all-blank table to be skipped, got %+v", sec)
		}
	}
}

func TestPptxExtractorNoTableProducesNoTableSections(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"Just plain text, no table"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "table") {
			t.Errorf("expected no table sections for a slide with no table, got %+v", sec)
		}
	}
}

func TestPptxExtractorMatch(t *testing.T) {
	e := &PptxExtractor{}
	if !e.Match("deck.pptx") || !e.Match("DECK.PPTX") {
		t.Error("expected .pptx (any case) to match")
	}
	if e.Match("deck.docx") {
		t.Error("expected .docx not to match")
	}
}

func TestPptxExtractorExtractsSlidesInOrder(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"First slide", "Second slide", "Third slide"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Shape != "3 slides" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "3 slides")
	}
	if len(res.Sections) != 3 {
		t.Fatalf("got %d sections, want 3: %+v", len(res.Sections), res.Sections)
	}
	wantLabels := []string{"slide 1", "slide 2", "slide 3"}
	wantTexts := []string{"First slide", "Second slide", "Third slide"}
	for i, sec := range res.Sections {
		if sec.Label != wantLabels[i] {
			t.Errorf("section %d label = %q, want %q", i, sec.Label, wantLabels[i])
		}
		if sec.Text != wantTexts[i] {
			t.Errorf("section %d text = %q, want %q", i, sec.Text, wantTexts[i])
		}
	}
}

// TestPptxExtractorSortsNumericallyNotLexically pins that slide10.xml
// sorts after slide2.xml, not before it — a naive filename string sort
// would put "slide10" before "slide2".
func TestPptxExtractorSortsNumericallyNotLexically(t *testing.T) {
	dir := t.TempDir()
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = fmt.Sprintf("slide-text-%d", i+1)
	}
	path := writePptxFixture(t, dir, texts)

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxPages: 10})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 10 {
		t.Fatalf("got %d sections, want 10", len(res.Sections))
	}
	if res.Sections[1].Text != "slide-text-2" || res.Sections[9].Text != "slide-text-10" {
		t.Errorf("expected numeric slide order, got %q then ... %q", res.Sections[1].Text, res.Sections[9].Text)
	}
}

func TestPptxExtractorOffsetSkipsSlides(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"one", "two", "three"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 2 || res.Sections[0].Label != "slide 2" {
		t.Fatalf("expected slides 2-3, got %+v", res.Sections)
	}
}

func TestPptxExtractorOffsetPastLastSlideIsReported(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"one", "two"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 5})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 0 {
		t.Errorf("expected no sections past the last slide, got %+v", res.Sections)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "past the last slide") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'past the last slide' warning, got %v", res.Warnings)
	}
}

func TestPptxExtractorMaxPagesLimitsSlideCount(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"one", "two", "three", "four"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxPages: 2})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 2 {
		t.Fatalf("expected 2 sections under MaxPages=2, got %+v", res.Sections)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "page cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a page-cap warning, got %v", res.Warnings)
	}
}

func TestPptxExtractorNoSlidesErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)
	_, _ = zw.Create("ppt/presentation.xml")
	zw.Close()
	f.Close()

	e := &PptxExtractor{}
	_, err = e.Extract(context.Background(), ExtractRequest{Path: path})
	if err == nil || !strings.Contains(err.Error(), "no ppt/slides") {
		t.Fatalf("expected a 'no slides' error, got %v", err)
	}
}

func TestPptxExtractorMalformedSlideReturnsPartialResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("ppt/slides/slide1.xml")
	_, _ = w.Write([]byte(`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:t>Before break`))
	zw.Close()
	f.Close()

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("expected a partial result, not an error: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "stopped parsing") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'stopped parsing' warning, got %v", res.Warnings)
	}
}

func TestPptxExtractorRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{strings.Repeat("x", 1000)})
	e := &PptxExtractor{}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-cap error, got %v", err)
	}
}
