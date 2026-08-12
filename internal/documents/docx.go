package documents

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DocxExtractor extracts bounded, character-windowed text from docx
// files. Two tiers, tried in fidelity order:
//
//  1. pandoc (when Run is set and succeeds): "pandoc -f docx -t gfm"
//     preserves real tables (as pipe tables), bold/italic, and nested
//     lists — the "full fidelity" tier of the docx parse fallback chain
//     in roadmap/document-generation.md.
//  2. Native zip+XML walk of word/document.xml (always available, no
//     external binary): flattened paragraph/heading text only, no
//     tables. Used when Run is nil or the pandoc tier fails for any
//     reason — never a hard error, this tier is always a valid degrade
//     target.
type DocxExtractor struct {
	// Run is nil-safe: a nil Run skips straight to the native tier,
	// mirroring PDFExtractor's own optional-CommandRunner shape.
	Run CommandRunner
}

func (e *DocxExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".docx")
}

func (e *DocxExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	if info.Size() > req.MaxBytes {
		return ExtractResult{}, fmt.Errorf("documents: docx %q (%d bytes) exceeds the %d-byte read cap", req.Path, info.Size(), req.MaxBytes)
	}

	if e.Run != nil {
		if res, ok := e.extractViaPandoc(ctx, req, info); ok {
			return res, nil
		}
	}
	return e.extractNative(ctx, req, info)
}

// extractViaPandoc is the richer, optional tier. Returns ok=false on any
// failure (Run erroring, pandoc missing, empty output) so the caller
// falls through to extractNative — this tier is additive, never a hard
// failure mode for the extractor as a whole.
func (e *DocxExtractor) extractViaPandoc(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, bool) {
	cmd := fmt.Sprintf("pandoc -f docx -t gfm %s", shellQuote(req.Path))
	out, _, err := e.Run(ctx, cmd)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return ExtractResult{}, false
	}

	text := newBoundedTextBuilder(req.Offset, req.MaxChars)
	text.Add(string(out))

	var warnings []string
	if notice := textWindowNotice(req.Offset, req.MaxChars, text.Total(), "extracted text"); notice != "" {
		warnings = append(warnings, notice)
	}
	preview := boundedString(text.String(), req.MaxChars)

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "docx",
			SizeBytes: info.Size(),
			// Shape itself signals which tier served the result, so a
			// caller is never left guessing at the fidelity of what it
			// got back.
			Shape: "pandoc-parsed (gfm): tables and inline formatting preserved",
		},
		Sections: []DocumentSection{{Label: "document body", Text: preview}},
		Warnings: warnings,
	}, true
}

// extractNative is the always-available tier: a hand-rolled zip+XML walk
// with no external dependency, matching the "structure only" fallback in
// roadmap/document-generation.md's docx parse chain.
func (e *DocxExtractor) extractNative(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, error) {
	zr, err := zip.OpenReader(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: open docx: %w", err)
	}
	defer zr.Close()

	entry := findZipEntry(zr.File, "word/document.xml")
	if entry == nil {
		return ExtractResult{}, fmt.Errorf("documents: docx %q has no word/document.xml", req.Path)
	}
	rc, err := entry.Open()
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: open word/document.xml: %w", err)
	}
	defer rc.Close()

	// Decompressed-size cap: a zip entry's declared compressed/
	// uncompressed size in the header isn't trustworthy on its own (a
	// crafted entry can lie), so this caps actual bytes read during
	// decompression instead of trusting the header.
	dec := xml.NewDecoder(io.LimitReader(rc, req.MaxBytes))

	text := newBoundedTextBuilder(req.Offset, req.MaxChars)
	var warnings []string
	var headingCount, paragraphCount int

	var curPara strings.Builder
	headingLevel := 0
	inParagraph := false
	first := true

	flush := func() {
		body := strings.TrimSpace(curPara.String())
		if body != "" {
			chunk := body
			if headingLevel > 0 {
				chunk = strings.Repeat("#", headingLevel) + " " + chunk
				headingCount++
			}
			if !first {
				// boundedTextBuilder.Add already separates consecutive
				// chunks with one space; prepending a blank line here
				// keeps paragraphs visually distinct in the preview
				// rather than running together.
				chunk = "\n\n" + chunk
			}
			first = false
			text.Add(chunk)
			paragraphCount++
		}
		curPara.Reset()
		headingLevel = 0
	}

	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("stopped parsing word/document.xml: %v", err))
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				inParagraph = true
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						if lvl, ok := headingStyleLevel(a.Value); ok {
							headingLevel = lvl
						}
					}
				}
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					curPara.WriteString(s)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inParagraph {
				flush()
				inParagraph = false
			}
		}
	}
	if inParagraph {
		flush()
	}

	if notice := textWindowNotice(req.Offset, req.MaxChars, text.Total(), "extracted text"); notice != "" {
		warnings = append(warnings, notice)
	}
	preview := boundedString(text.String(), req.MaxChars)

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "docx",
			SizeBytes: info.Size(),
			Shape:     fmt.Sprintf("%d paragraphs (%d headings)", paragraphCount, headingCount),
		},
		Sections: []DocumentSection{{Label: "document body", Text: preview}},
		Warnings: warnings,
	}, nil
}

// headingStyleLevel reports the heading level a WordprocessingML
// paragraph style value encodes (Word's own "Heading1".."Heading9"
// convention), or false for a body-text/other style.
func headingStyleLevel(styleVal string) (int, bool) {
	if !strings.HasPrefix(styleVal, "Heading") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(styleVal, "Heading"))
	if err != nil || n < 1 || n > 9 {
		return 0, false
	}
	return n, true
}

// findZipEntry returns the zip.File named exactly name, or nil.
func findZipEntry(files []*zip.File, name string) *zip.File {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}
