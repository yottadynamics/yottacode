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
//     in docs/document-generation.md.
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

	res, err := e.extractText(ctx, req, info)
	if err != nil {
		return ExtractResult{}, err
	}

	// Image metadata is collected independently of which text tier
	// served the body: pandoc's gfm output doesn't reliably carry
	// enough of this (no --extract-media is passed, so it wouldn't
	// even resolve to a real file), and the native tier's own zip/XML
	// walk is scoped to text/heading extraction only. One extra, cheap
	// pass either way — see extractDocxImageSections.
	imgSections, imgWarn := extractDocxImageSections(req.Path, req.MaxBytes)
	if imgWarn != "" {
		res.Warnings = append(res.Warnings, imgWarn)
	}
	res.Sections, res.Warnings = appendSectionsWithinCharCap(res.Sections, res.Warnings, imgSections, req.MaxChars, "docx image section")
	return res, nil
}

func (e *DocxExtractor) extractText(ctx context.Context, req ExtractRequest, info os.FileInfo) (ExtractResult, error) {
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
// docs/document-generation.md's docx parse chain.
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

// extractDocxImageSections returns "document image M" metadata
// sections (file name, size, alt text — never image bytes) for every
// inline picture found in word/document.xml, resolved against
// word/_rels/document.xml.rels. A missing/unreadable document.xml or
// rels part degrades to no image sections (with a warning for the
// former, silently for the latter — mirroring resolveImageMediaFiles'
// own "no rels part" tolerance for pptx) rather than turning an
// otherwise-successful Extract into an error.
func extractDocxImageSections(path string, maxBytes int64) ([]DocumentSection, string) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, ""
	}
	defer zr.Close()

	entry := findZipEntry(zr.File, "word/document.xml")
	if entry == nil {
		return nil, ""
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Sprintf("could not read word/document.xml for image metadata: %v", err)
	}
	images, warn := extractDocxImages(rc, maxBytes)
	rc.Close()
	if warn != "" {
		warn = "image metadata: " + warn
	}
	if len(images) == 0 {
		return nil, warn
	}

	if relsEntry := findZipEntry(zr.File, "word/_rels/document.xml.rels"); relsEntry != nil {
		if err := resolveImageMediaFiles(images, relsEntry, maxBytes); err != nil && warn == "" {
			warn = fmt.Sprintf("could not resolve image media files: %v", err)
		}
	}

	return buildDocxImageSections(images), warn
}

// extractDocxImages walks r's XML tokens once, collecting metadata for
// every embedded picture in word/document.xml. WordprocessingML wraps
// a picture in <w:drawing><wp:inline>, but the picture itself uses the
// exact same DrawingML <pic:pic>/<a:blip r:embed=".."/>/<a:xfrm><a:ext
// cx=".." cy=".."/></a:xfrm> schema pptx slides use — matched by local
// element name only (same approach extractPptxSlideContent's own
// picture handling takes), which is why this reads as a near-mirror of
// that function's image-tracking branches rather than sharing a
// decoder loop with it: pptx's walk is interleaved with its own text/
// table tracking in one pass, so factoring a shared sub-walker would
// complicate already-tested code for a ~30-line block used in two
// different loop shapes.
func extractDocxImages(r io.Reader, maxBytes int64) (images []drawingMLPicture, warn string) {
	if maxBytes <= 0 {
		return nil, "skipped: decompressed-size cap already exhausted"
	}
	dec := xml.NewDecoder(io.LimitReader(r, maxBytes))

	inPic := false
	var curImage *drawingMLPicture

	attr := func(t xml.StartElement, local string) (string, bool) {
		for _, a := range t.Attr {
			if a.Name.Local == local {
				return a.Value, true
			}
		}
		return "", false
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return images, fmt.Sprintf("stopped parsing: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "pic":
				images = append(images, drawingMLPicture{})
				curImage = &images[len(images)-1]
				inPic = true
			case "cNvPr":
				if inPic && curImage != nil {
					if v, ok := attr(t, "descr"); ok {
						curImage.AltText = v
					}
				}
			case "blip":
				if inPic && curImage != nil {
					if v, ok := attr(t, "embed"); ok {
						curImage.EmbedID = v
					}
				}
			case "ext":
				if inPic && curImage != nil {
					if v, ok := attr(t, "cx"); ok {
						if n, err := strconv.ParseInt(v, 10, 64); err == nil {
							curImage.WidthEMU = n
						}
					}
					if v, ok := attr(t, "cy"); ok {
						if n, err := strconv.ParseInt(v, 10, 64); err == nil {
							curImage.HeightEMU = n
						}
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "pic" {
				inPic = false
				curImage = nil
			}
		}
	}
	return images, ""
}

// buildDocxImageSections renders every picture found in the document
// as a labeled DocumentSection: "document image M". An image with no
// extractable metadata at all (a false-positive <pic> match) is
// skipped, mirroring buildPptxImageSections' own skip.
func buildDocxImageSections(images []drawingMLPicture) []DocumentSection {
	var sections []DocumentSection
	for i, img := range images {
		text, ok := imageSectionText(img)
		if !ok {
			continue
		}
		sections = append(sections, DocumentSection{
			Label: fmt.Sprintf("document image %d", i+1),
			Text:  text,
		})
	}
	return sections
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
