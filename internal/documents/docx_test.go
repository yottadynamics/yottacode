package documents

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeDocxFixture builds a minimal, hand-rolled .docx: a zip archive
// with a single word/document.xml entry holding the given body XML.
// Real docx files carry more parts ([Content_Types].xml, _rels, styles,
// etc.), but DocxExtractor only ever reads word/document.xml, so this is
// the whole surface worth faking — no binary fixture needs to be
// checked in.
func writeDocxFixture(t *testing.T, dir, bodyXML string) string {
	t.Helper()
	path := filepath.Join(dir, "doc.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	xmlDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		bodyXML +
		`</w:body></w:document>`
	if _, err := w.Write([]byte(xmlDoc)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return path
}

func docxParagraph(text string) string {
	return `<w:p><w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func docxHeading(level int, text string) string {
	return `<w:p><w:pPr><w:pStyle w:val="Heading` + strconv.Itoa(level) + `"/></w:pPr><w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func TestDocxExtractorMatch(t *testing.T) {
	e := &DocxExtractor{}
	if !e.Match("report.docx") || !e.Match("REPORT.DOCX") {
		t.Error("expected .docx (any case) to match")
	}
	if e.Match("report.pdf") {
		t.Error("expected .pdf not to match")
	}
}

func TestDocxExtractorExtractsParagraphsAndHeadings(t *testing.T) {
	dir := t.TempDir()
	body := docxHeading(1, "Title") + docxParagraph("Hello world.")
	path := writeDocxFixture(t, dir, body)

	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 1 {
		t.Fatalf("expected one 'document body' section, got %+v", res.Sections)
	}
	text := res.Sections[0].Text
	if !strings.Contains(text, "# Title") {
		t.Errorf("expected heading rendered as '# Title', got %q", text)
	}
	if !strings.Contains(text, "Hello world.") {
		t.Errorf("expected paragraph text, got %q", text)
	}
	if !strings.Contains(res.Metadata.Shape, "2 paragraphs") || !strings.Contains(res.Metadata.Shape, "1 headings") {
		t.Errorf("Shape = %q, want counts for 2 paragraphs / 1 heading", res.Metadata.Shape)
	}
}

func TestDocxExtractorMultipleRunsJoinWithinParagraph(t *testing.T) {
	dir := t.TempDir()
	body := `<w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t>world.</w:t></w:r></w:p>`
	path := writeDocxFixture(t, dir, body)

	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Sections[0].Text, "Hello world.") {
		t.Errorf("expected split runs joined into one line, got %q", res.Sections[0].Text)
	}
}

func TestDocxExtractorNoDocumentXMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)
	_, _ = zw.Create("word/styles.xml")
	zw.Close()
	f.Close()

	e := &DocxExtractor{}
	_, err = e.Extract(context.Background(), ExtractRequest{Path: path})
	if err == nil || !strings.Contains(err.Error(), "no word/document.xml") {
		t.Fatalf("expected a 'no word/document.xml' error, got %v", err)
	}
}

func TestDocxExtractorMalformedXMLReturnsPartialResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("word/document.xml")
	// Well-formed opening, then truncated mid-element — no closing tags.
	_, _ = w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Before break`))
	zw.Close()
	f.Close()

	e := &DocxExtractor{}
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

func TestDocxExtractorRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph(strings.Repeat("x", 1000)))
	e := &DocxExtractor{}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-cap error, got %v", err)
	}
}

func TestDocxExtractorPandocTierUsedWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph("native content, should not appear"))

	var seenCmd string
	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		seenCmd = command
		return []byte("# Heading\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n"), nil, nil
	}
	e := &DocxExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(seenCmd, "pandoc -f docx -t gfm") {
		t.Errorf("expected a pandoc -f docx -t gfm command, got %q", seenCmd)
	}
	if !strings.Contains(res.Sections[0].Text, "| A | B |") {
		t.Errorf("expected pandoc's table output to be used, got %q", res.Sections[0].Text)
	}
	if !strings.Contains(res.Metadata.Shape, "pandoc-parsed") {
		t.Errorf("expected Shape to name the pandoc tier, got %q", res.Metadata.Shape)
	}
}

func TestDocxExtractorFallsBackToNativeWhenPandocErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph("native fallback content"))

	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		return nil, []byte("pandoc: command not found"), errors.New("exit status 127")
	}
	e := &DocxExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("expected a fallback to native, not an error: %v", err)
	}
	if !strings.Contains(res.Sections[0].Text, "native fallback content") {
		t.Errorf("expected native-tier content after pandoc failure, got %q", res.Sections[0].Text)
	}
	if strings.Contains(res.Metadata.Shape, "pandoc-parsed") {
		t.Errorf("expected Shape to name the native tier, got %q", res.Metadata.Shape)
	}
}

func TestDocxExtractorFallsBackToNativeOnEmptyPandocOutput(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph("native content on empty pandoc output"))

	run := func(ctx context.Context, command string) ([]byte, []byte, error) {
		return []byte("   \n"), nil, nil
	}
	e := &DocxExtractor{Run: run}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Sections[0].Text, "native content on empty pandoc output") {
		t.Errorf("expected native-tier fallback on blank pandoc output, got %q", res.Sections[0].Text)
	}
}

func TestDocxExtractorNilRunUsesNativeTier(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph("native only"))
	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Sections[0].Text, "native only") {
		t.Errorf("expected native-tier content with a nil Run, got %q", res.Sections[0].Text)
	}
}

// writeDocxImageFixture builds a .docx whose body has a paragraph
// followed by an inline drawing wrapping a <pic:pic> that references
// rId1 — with an accompanying word/_rels/document.xml.rels part
// mapping rId1 to mediaTarget. WordprocessingML's real inline-drawing
// markup nests more wrapper elements (<w:drawing><wp:inline>...) than
// this includes, but extractDocxImages matches by local element name
// only, so the wrapper's exact shape doesn't matter — only <pic:pic>/
// <a:blip r:embed>/<a:xfrm><a:ext> do.
func writeDocxImageFixture(t *testing.T, dir, bodyText, alt, mediaTarget string, cx, cy int64) string {
	t.Helper()
	path := filepath.Join(dir, "doc.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	body := docxParagraph(bodyText) +
		`<w:p><w:r><w:drawing><wp:inline>` +
		`<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">` +
		`<pic:nvPicPr><pic:cNvPr id="1" name="Picture" descr="` + alt + `"/><pic:cNvPicPr/></pic:nvPicPr>` +
		`<pic:blipFill><a:blip r:embed="rId1" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></pic:blipFill>` +
		`<pic:spPr><a:xfrm xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:ext cx="` + fmt.Sprint(cx) + `" cy="` + fmt.Sprint(cy) + `"/></a:xfrm></pic:spPr>` +
		`</pic:pic>` +
		`</wp:inline></w:drawing></w:r></w:p>`
	xmlDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body + `</w:body></w:document>`
	if _, err := w.Write([]byte(xmlDoc)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}

	if mediaTarget != "" {
		rw, err := zw.Create("word/_rels/document.xml.rels")
		if err != nil {
			t.Fatalf("create rels entry: %v", err)
		}
		relsDoc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + mediaTarget + `"/>` +
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

func TestDocxExtractorImageSectionRendered(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxImageFixture(t, dir, "intro text", "A diagram", "media/image1.png", 5029200, 3429000)

	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var imgSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "document image 1" {
			imgSec = &res.Sections[i]
		}
	}
	if imgSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "document image 1", res.Sections)
	}
	for _, want := range []string{"file: image1.png", "size: 5.50in x 3.75in", "alt: A diagram"} {
		if !strings.Contains(imgSec.Text, want) {
			t.Errorf("image section text %q missing %q", imgSec.Text, want)
		}
	}
	// Regular text extraction must be unaffected by the image pass.
	if !strings.Contains(res.Sections[0].Text, "intro text") {
		t.Errorf("expected body text to still be extracted, got %q", res.Sections[0].Text)
	}
}

func TestDocxExtractorNoImageProducesNoImageSections(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxFixture(t, dir, docxParagraph("no pictures here"))

	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "image") {
			t.Errorf("expected no image sections for a document with no picture, got %+v", sec)
		}
	}
}

// TestDocxExtractorImageSectionSurvivesPandocTier confirms image
// metadata is present even when the richer pandoc text tier is what
// actually served the body — the two must not be mutually exclusive.
func TestDocxExtractorImageSectionSurvivesPandocTier(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxImageFixture(t, dir, "intro", "Chart", "media/image1.png", 914400, 914400)

	e := &DocxExtractor{Run: func(ctx context.Context, cmd string) ([]byte, []byte, error) {
		return []byte("pandoc body text"), nil, nil
	}}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Shape != "pandoc-parsed (gfm): tables and inline formatting preserved" {
		t.Fatalf("expected the pandoc tier to have served this result, got shape %q", res.Metadata.Shape)
	}
	found := false
	for _, sec := range res.Sections {
		if sec.Label == "document image 1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected image metadata alongside pandoc-tier text, got sections %+v", res.Sections)
	}
}

func TestDocxExtractorImageSectionsRespectMaxChars(t *testing.T) {
	dir := t.TempDir()
	path := writeDocxImageFixture(t, dir, "body", strings.Repeat("alt", 20), "media/image1.png", 914400, 914400)

	e := &DocxExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxChars: 10})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	total := 0
	for _, sec := range res.Sections {
		total += len(sec.Text)
	}
	if total > 10 {
		t.Fatalf("sections used %d chars, want <= 10: %+v", total, res.Sections)
	}
	if !containsWarning(res.Warnings, "preview cap") {
		t.Fatalf("expected a preview-cap warning, got %v", res.Warnings)
	}
}

func TestHeadingStyleLevel(t *testing.T) {
	cases := map[string]struct {
		level int
		ok    bool
	}{
		"Heading1":   {1, true},
		"Heading9":   {9, true},
		"Heading10":  {0, false},
		"Normal":     {0, false},
		"":           {0, false},
		"HeadingFoo": {0, false},
	}
	for in, want := range cases {
		level, ok := headingStyleLevel(in)
		if level != want.level || ok != want.ok {
			t.Errorf("headingStyleLevel(%q) = (%d, %v), want (%d, %v)", in, level, ok, want.level, want.ok)
		}
	}
}
