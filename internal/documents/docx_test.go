package documents

import (
	"archive/zip"
	"context"
	"errors"
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
