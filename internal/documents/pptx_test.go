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

// writePptxImageFixture builds a single-slide .pptx whose slide has a text
// shape (bodyText) followed by a <p:pic> referencing rId2 — with an
// accompanying ppt/slides/_rels/slide1.xml.rels part mapping rId2 to
// mediaTarget, mirroring what GeneratePPTX itself writes (see
// pptxPictureShape/pptxSlideRelsXML) closely enough that the extractor's
// rels-resolution path is exercised the same way a real generated deck
// would exercise it, without needing the full real pptx part set.
func writePptxImageFixture(t *testing.T, dir, bodyText, alt, mediaTarget string, cx, cy int64) string {
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
	xmlDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + bodyText + `</a:t></a:r></a:p></p:txBody></p:sp>` +
		`<p:pic><p:nvPicPr><p:cNvPr id="3" name="Picture" descr="` + alt + `"/><p:cNvPicPr/><p:nvPr/></p:nvPicPr>` +
		`<p:blipFill><a:blip r:embed="rId2"/></p:blipFill>` +
		`<p:spPr><a:xfrm><a:off x="100" y="200"/><a:ext cx="` + fmt.Sprint(cx) + `" cy="` + fmt.Sprint(cy) + `"/></a:xfrm></p:spPr>` +
		`</p:pic>` +
		`</p:spTree></p:cSld></p:sld>`
	if _, err := w.Write([]byte(xmlDoc)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}

	if mediaTarget != "" {
		rw, err := zw.Create("ppt/slides/_rels/slide1.xml.rels")
		if err != nil {
			t.Fatalf("create rels entry: %v", err)
		}
		relsDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + mediaTarget + `"/>` +
			`</Relationships>`
		if _, err := rw.Write([]byte(relsDoc)); err != nil {
			t.Fatalf("write rels entry: %v", err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return path
}

func TestPptxExtractorImageSectionRendered(t *testing.T) {
	dir := t.TempDir()
	path := writePptxImageFixture(t, dir, "Slide intro text", "A bar chart", "../media/image1.png", 5029200, 3429000)

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var imgSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "slide 1 image 1" {
			imgSec = &res.Sections[i]
		}
	}
	if imgSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "slide 1 image 1", res.Sections)
	}
	for _, want := range []string{"file: image1.png", "size: 5.50in x 3.75in", "alt: A bar chart"} {
		if !strings.Contains(imgSec.Text, want) {
			t.Errorf("image section text %q missing %q", imgSec.Text, want)
		}
	}
}

// TestPptxExtractorImageSectionNoRelsStillReportsMetadata confirms a
// missing/unresolvable rels part degrades gracefully: alt text and
// dimensions (read straight off the slide XML, no rels needed) still come
// back, just without a resolved file name — not an error, not a dropped
// section.
func TestPptxExtractorImageSectionNoRelsStillReportsMetadata(t *testing.T) {
	dir := t.TempDir()
	path := writePptxImageFixture(t, dir, "Slide intro text", "A bar chart", "", 5029200, 3429000)

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var imgSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "slide 1 image 1" {
			imgSec = &res.Sections[i]
		}
	}
	if imgSec == nil {
		t.Fatalf("expected a %q section even with no rels part, got: %+v", "slide 1 image 1", res.Sections)
	}
	if strings.Contains(imgSec.Text, "file:") {
		t.Errorf("expected no resolved file name without a rels part, got %q", imgSec.Text)
	}
	if !strings.Contains(imgSec.Text, "alt: A bar chart") {
		t.Errorf("expected alt text to still be reported, got %q", imgSec.Text)
	}
}

func TestPptxExtractorNoImageProducesNoImageSections(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{"Just plain text, no picture"})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "image") {
			t.Errorf("expected no image sections for a slide with no picture, got %+v", sec)
		}
	}
}

// TestPptxExtractorImageBytesNeverInSections is the "never image bytes"
// constraint from issue #6: no section's text may contain anything that
// looks like the embedded media's own content, only metadata about it.
func TestPptxExtractorImageBytesNeverInSections(t *testing.T) {
	dir := t.TempDir()
	path := writePptxImageFixture(t, dir, "intro", "alt text", "../media/image1.png", 100, 100)

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Text, "PNG") || strings.Contains(sec.Text, "\x89") {
			t.Errorf("section %q appears to contain image bytes: %q", sec.Label, sec.Text)
		}
	}
}

// TestPptxExtractorReadsGeneratedDeckImageMetadata round-trips a real
// GeneratePPTX output (not a hand-rolled fixture) through PptxExtractor,
// confirming the parser's rels-resolution path matches what the generator
// actually writes (pptxSlideRelsXML's Target, pptxPictureShape's descr and
// a:ext) — not just an assumed shape a fixture could quietly drift from.
func TestPptxExtractorReadsGeneratedDeckImageMetadata(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "chart.png")
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(imgPath, png, 0o644); err != nil {
		t.Fatalf("write test png: %v", err)
	}

	data, err := GeneratePPTX([]SlideModel{{Title: "Report", Image: imgPath, ImageAlt: "Chart of results"}})
	if err != nil {
		t.Fatalf("GeneratePPTX: %v", err)
	}
	deckPath := filepath.Join(dir, "deck.pptx")
	if err := os.WriteFile(deckPath, data, 0o644); err != nil {
		t.Fatalf("write generated deck: %v", err)
	}

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: deckPath})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var imgSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "slide 1 image 1" {
			imgSec = &res.Sections[i]
		}
	}
	if imgSec == nil {
		t.Fatalf("expected a %q section reading back a real generated deck, got: %+v", "slide 1 image 1", res.Sections)
	}
	for _, want := range []string{"file: image1.png", "alt: Chart of results", "size: 5.50in x 3.75in"} {
		if !strings.Contains(imgSec.Text, want) {
			t.Errorf("generated-deck image section %q missing %q", imgSec.Text, want)
		}
	}
}

// TestPptxExtractorMaxBytesIsCumulativeOverSlideXML guards the zip-bomb
// boundary: MaxBytes is a cumulative decompressed XML budget across slides,
// not a per-slide budget and not a text-length budget.
func TestPptxExtractorMaxBytesIsCumulativeOverSlideXML(t *testing.T) {
	dir := t.TempDir()
	path := writePptxFixture(t, dir, []string{
		strings.Repeat("A", 1000),
		strings.Repeat("B", 1000),
	})

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 800})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(res.Sections) > 1 && strings.Contains(res.Sections[1].Text, "BBBB") {
		t.Fatalf("expected the cumulative XML byte budget to stop before reading slide 2 text, got sections: %+v", res.Sections)
	}
	if !containsWarning(res.Warnings, "decompressed-size cap") {
		t.Fatalf("expected a decompressed-size warning, got %v", res.Warnings)
	}
}

func TestPptxExtractorRelsResolutionRespectsRemainingMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("create slide: %v", err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:pic><p:nvPicPr><p:cNvPr descr="A chart"/></p:nvPicPr><p:blipFill><a:blip r:embed="rId2"/></p:blipFill><p:spPr><a:xfrm><a:ext cx="914400" cy="914400"/></a:xfrm></p:spPr></p:pic></p:sld>`))
	rw, err := zw.Create("ppt/slides/_rels/slide1.xml.rels")
	if err != nil {
		t.Fatalf("create rels: %v", err)
	}
	_, _ = rw.Write([]byte(`<Relationships>` + strings.Repeat(`<Relationship Id="x" Target="../media/x.png"/>`, 200) + `<Relationship Id="rId2" Target="../media/image1.png"/></Relationships>`))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	e := &PptxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 700})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !containsWarning(res.Warnings, "decompressed-size cap") {
		t.Fatalf("expected a decompressed-size warning for rels resolution, got %v", res.Warnings)
	}
	if !hasSectionLabel(res.Sections, "slide 1 image 1") {
		t.Fatalf("expected image metadata to survive capped rels parsing, got sections: %+v", res.Sections)
	}
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
