package documents

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PptxExtractor extracts bounded, slide-labeled text from pptx files: a
// zip archive with one ppt/slides/slideN.xml entry per slide. Native
// zip+XML walk (no external binary) — text extraction matches the
// "text only" tier of the pptx parse fallback chain in
// docs/document-generation.md, additionally augmented with real
// DrawingML table structure (see extractPptxSlideContent) so a table's
// cell text comes back as clean rows, not just run together with every
// other word on the slide, and with picture metadata (file name, size,
// alt text — never image bytes; see buildPptxImageSections) for every
// <p:pic> found on a slide.
type PptxExtractor struct{}

func (e *PptxExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pptx")
}

var pptxSlideNameRE = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
var pptxSlideRelsNameRE = regexp.MustCompile(`^ppt/slides/_rels/slide(\d+)\.xml\.rels$`)

func (e *PptxExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	if info.Size() > req.MaxBytes {
		return ExtractResult{}, fmt.Errorf("documents: pptx %q (%d bytes) exceeds the %d-byte read cap", req.Path, info.Size(), req.MaxBytes)
	}

	zr, err := zip.OpenReader(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: open pptx: %w", err)
	}
	defer zr.Close()

	type slideFile struct {
		num int
		f   *zip.File
	}
	var slides []slideFile
	for _, f := range zr.File {
		if m := pptxSlideNameRE.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slideFile{num: n, f: f})
		}
	}
	if len(slides) == 0 {
		return ExtractResult{}, fmt.Errorf("documents: pptx %q has no ppt/slides/slideN.xml entries", req.Path)
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	// Indexed once up front (not opened lazily per slide) so a slide
	// with no images pays nothing beyond this one map-population pass —
	// the same "index during the existing zr.File walk, resolve only
	// when needed" shape the slides regex loop above already uses.
	relsFiles := map[int]*zip.File{}
	for _, f := range zr.File {
		if m := pptxSlideRelsNameRE.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			relsFiles[n] = f
		}
	}

	totalSlides := len(slides)
	if req.Offset >= totalSlides {
		return ExtractResult{
			Metadata: DocumentMetadata{Kind: "pptx", SizeBytes: info.Size(), Shape: fmt.Sprintf("%d slides", totalSlides)},
			Warnings: []string{fmt.Sprintf("offset %d is past the last slide (%d total)", req.Offset, totalSlides)},
		}, nil
	}

	endIdx := req.Offset + req.MaxPages
	if endIdx > totalSlides {
		endIdx = totalSlides
	}

	var warnings []string
	var texts []string
	var tableSections []DocumentSection
	var imageSections []DocumentSection
	// Decompressed-size cap: bounds cumulative bytes read across every
	// slide entry, not just one — a crafted archive can't defeat the
	// cap by spreading bulk across many small-looking slide entries.
	budget := req.MaxBytes
	for _, se := range slides[req.Offset:endIdx] {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		rc, err := se.f.Open()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: %v", se.num, err))
			texts = append(texts, "")
			continue
		}
		text, tables, images, bytesRead, slideWarn := extractPptxSlideContent(rc, budget)
		rc.Close()
		if slideWarn != "" {
			warnings = append(warnings, fmt.Sprintf("slide %d: %s", se.num, slideWarn))
		}
		budget -= bytesRead
		if budget < 0 {
			budget = 0
		}
		texts = append(texts, text)
		tableSections = append(tableSections, buildPptxTableSections(se.num, tables)...)
		if len(images) > 0 {
			resolveErr := resolveImageMediaFiles(images, relsFiles[se.num], budget)
			if resolveErr != nil {
				warnings = append(warnings, fmt.Sprintf("slide %d: could not resolve image media files: %v", se.num, resolveErr))
			}
			imageSections = append(imageSections, buildPptxImageSections(se.num, images)...)
		}
	}

	sections, sectionWarnings := buildLabeledUnitSections(texts, req.Offset+1, req.MaxChars, "slide")
	warnings = append(warnings, sectionWarnings...)
	// Additive, same as PDF's table tier: a slide's table cell text
	// already appears in its plain "slide N" section above (run
	// together with every other word, same fidelity pdftotext's own
	// plain-text tier has for a PDF table); this adds a second, cleanly
	// structured rendering alongside it rather than replacing anything.
	sections, warnings = appendSectionsWithinCharCap(sections, warnings, tableSections, req.MaxChars, "pptx table section")
	// Metadata only — never image bytes. A consumer that wants the actual
	// picture reads media/<file> via read_file explicitly.
	sections, warnings = appendSectionsWithinCharCap(sections, warnings, imageSections, req.MaxChars, "pptx image section")

	if endIdx < totalSlides {
		warnings = append(warnings, fmt.Sprintf("showing slides %d-%d of %d (page cap)", req.Offset+1, endIdx, totalSlides))
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "pptx",
			SizeBytes: info.Size(),
			Shape:     fmt.Sprintf("%d slides", totalSlides),
		},
		Sections: sections,
		Warnings: warnings,
	}, nil
}

// pptxTable is one DrawingML <a:tbl> found on a slide: plain rows, no
// header/body distinction — the OOXML table markup doesn't reliably
// mark a header row (some do via <a:tblPr firstRow="1">, most don't),
// same reasoning PDF table extraction's own pagePDFExtraction type already
// documents for pdfplumber's output.
type pptxTable struct {
	Rows [][]string
}

// drawingMLPicture is one <p:pic> found on a slide: alt text and dimensions
// read directly off the slide XML, plus the relationship ID its
// <a:blip r:embed="..."> points at — resolved to an actual media
// filename separately (see resolveImageMediaFiles), since that
// requires the slide's own _rels/slideN.xml.rels part, not just the
// slide XML this struct is built from.
type drawingMLPicture struct {
	AltText   string
	EmbedID   string
	MediaFile string
	WidthEMU  int64
	HeightEMU int64
}

// extractPptxSlideContent walks one slide's XML once, returning its
// flat text (every <a:t> run joined with a space, exactly what
// extractPptxSlideText always returned — a table's cell text is still
// included here, run together with everything else, unchanged), any
// DrawingML tables found on the slide as clean rows, and any pictures
// found on the slide as drawingMLPicture metadata (no image bytes read —
// this only touches the slide's own XML), all tracked in one pass.
// maxBytes caps this slide's share of the overall decompressed-size
// budget.
//
// A table nested inside another table's cell (vanishingly rare in
// real decks) is not tracked as its own separate table — its text
// still reaches the flat output and the outer cell's text, just not a
// distinct nested pptxTable; documented trade-off, not a bug to fix
// later, mirroring extract_pdf_tables.py's own documented shortcuts.
func extractPptxSlideContent(r io.Reader, maxBytes int64) (text string, tables []pptxTable, images []drawingMLPicture, bytesRead int64, warn string) {
	if maxBytes <= 0 {
		return "", nil, nil, 0, "skipped: decompressed-size cap already exhausted by earlier slides"
	}
	counting := &countingReader{r: io.LimitReader(r, maxBytes)}
	dec := xml.NewDecoder(counting)
	var b strings.Builder
	warn = ""
	defer func() { bytesRead = counting.n }()

	tableDepth := 0
	var curTable *pptxTable
	var curRow []string
	var curCell strings.Builder
	inCell := false

	inPic := false
	var curImage *drawingMLPicture

	appendText := func(s string) {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(s)
		if inCell {
			if curCell.Len() > 0 {
				curCell.WriteString(" ")
			}
			curCell.WriteString(s)
		}
	}

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
			return strings.TrimSpace(b.String()), tables, images, counting.n, fmt.Sprintf("stopped parsing: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				tableDepth++
				if tableDepth == 1 {
					tables = append(tables, pptxTable{})
					curTable = &tables[len(tables)-1]
				}
			case "tr":
				if tableDepth == 1 {
					curRow = nil
				}
			case "tc":
				if tableDepth == 1 {
					inCell = true
					curCell.Reset()
				}
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					appendText(s)
				}
			case "pic":
				images = append(images, drawingMLPicture{})
				curImage = &images[len(images)-1]
				inPic = true
			case "cNvPr":
				// <p:cNvPr descr="..."/> also appears outside a <p:pic>
				// (every shape has one, including the slide's own root
				// group); inPic scopes this to the picture's own name/
				// alt-text element specifically.
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
				// <a:ext cx=".." cy=".."/> inside <a:xfrm> also appears
				// on text-box shapes; inPic scopes this to the
				// picture's own size specifically.
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
			switch t.Name.Local {
			case "tc":
				if tableDepth == 1 {
					inCell = false
					curRow = append(curRow, strings.TrimSpace(curCell.String()))
				}
			case "tr":
				if tableDepth == 1 && curTable != nil {
					curTable.Rows = append(curTable.Rows, curRow)
				}
			case "tbl":
				if tableDepth == 1 {
					curTable = nil
				}
				tableDepth--
			case "pic":
				inPic = false
				curImage = nil
			}
		}
	}
	return strings.TrimSpace(b.String()), tables, images, counting.n, ""
}

// ooxmlRelationships is the minimal shape of a
// ppt/slides/_rels/slideN.xml.rels part: enough to map a relationship
// ID (what <a:blip r:embed="..."> points at) to its Target path.
type ooxmlRelationships struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

// resolveImageMediaFiles fills in images[i].MediaFile for every
// image with a non-empty EmbedID, by reading relsFile (slideN's own
// _rels/slideN.xml.rels part) and mapping each relationship ID to its
// Target's base filename (Target is a path like "../media/image1.png";
// only the filename is useful to a caller, not the OOXML-internal
// relative path). relsFile == nil (a slide with pictures but somehow
// no rels part — malformed, or a hand-built fixture) leaves every
// MediaFile empty and returns nil: not an error, since alt text and
// dimensions are still useful metadata on their own.
func resolveImageMediaFiles(images []drawingMLPicture, relsFile *zip.File, maxBytes int64) error {
	if relsFile == nil {
		return nil
	}
	if maxBytes <= 0 {
		return fmt.Errorf("rels skipped: decompressed-size cap already exhausted")
	}
	rc, err := relsFile.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	counting := &countingReader{r: io.LimitReader(rc, maxBytes)}
	var rels ooxmlRelationships
	if err := xml.NewDecoder(counting).Decode(&rels); err != nil {
		if counting.n >= maxBytes && hasMoreAfterCap(rc) {
			return fmt.Errorf("parse %s: decompressed-size cap exhausted: %w", relsFile.Name, err)
		}
		return fmt.Errorf("parse %s: %w", relsFile.Name, err)
	}
	// If the relationship part exactly consumed its budget, report that
	// filename resolution may be incomplete rather than silently trusting
	// a potentially truncated rels XML from a crafted archive.
	if counting.n >= maxBytes && hasMoreAfterCap(rc) {
		return fmt.Errorf("parse %s: decompressed-size cap exhausted", relsFile.Name)
	}
	targets := make(map[string]string, len(rels.Relationships))
	for _, r := range rels.Relationships {
		targets[r.ID] = r.Target
	}
	for i := range images {
		if images[i].EmbedID == "" {
			continue
		}
		if target, ok := targets[images[i].EmbedID]; ok {
			images[i].MediaFile = filepath.Base(target)
		}
	}
	return nil
}

// imageSectionText renders one drawingMLPicture's metadata — media
// filename, size (EMU converted to inches, matching the unit
// create_document's image_left/image_width et al. already use), and
// alt text where present — as the text every format's image-metadata
// section shares (see buildPptxImageSections and DocxExtractor's
// buildDocxImageSections). Never image bytes: a consumer that wants
// the actual picture reads the media file via read_file explicitly,
// same boundary PDF OCR's own "text only, never the scanned image"
// already draws. ok=false when none of the three fields resolved (a
// false-positive <pic> match with nothing extractable), so a caller
// can skip emitting an empty section.
func imageSectionText(img drawingMLPicture) (string, bool) {
	var parts []string
	if img.MediaFile != "" {
		parts = append(parts, "file: "+img.MediaFile)
	}
	if img.WidthEMU > 0 && img.HeightEMU > 0 {
		parts = append(parts, fmt.Sprintf("size: %.2fin x %.2fin", emuToInches(img.WidthEMU), emuToInches(img.HeightEMU)))
	}
	if alt := strings.TrimSpace(img.AltText); alt != "" {
		parts = append(parts, "alt: "+alt)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ", "), true
}

// buildPptxImageSections renders every picture found on slide slideNum
// as a labeled DocumentSection: "slide N image M". An image with no
// extractable metadata at all (a false-positive <p:pic> match) is
// skipped, mirroring buildPptxTableSections' empty-table skip.
func buildPptxImageSections(slideNum int, images []drawingMLPicture) []DocumentSection {
	var sections []DocumentSection
	for i, img := range images {
		text, ok := imageSectionText(img)
		if !ok {
			continue
		}
		sections = append(sections, DocumentSection{
			Label: fmt.Sprintf("slide %d image %d", slideNum, i+1),
			Text:  text,
		})
	}
	return sections
}

func emuToInches(emu int64) float64 {
	return float64(emu) / float64(EMUPerInch)
}

// buildPptxTableSections renders every non-empty table on slide
// slideNum as a labeled DocumentSection: "slide N table M", pipe-
// joined rows — the same label/render shape buildTableSections already
// established for PDF, so read_document's table output reads
// consistently across formats. A table with zero non-blank rows (a
// false-positive <a:tbl> match, or one pandoc/PowerPoint left empty)
// is skipped, and a fully-blank row within a real table is dropped —
// same filtering extract_pdf_tables.py already applies.
func buildPptxTableSections(slideNum int, tables []pptxTable) []DocumentSection {
	var sections []DocumentSection
	for i, table := range tables {
		var rows [][]string
		for _, row := range table.Rows {
			if anyNonEmpty(row) {
				rows = append(rows, row)
			}
		}
		if len(rows) == 0 {
			continue
		}
		var sb strings.Builder
		for r, row := range rows {
			if r > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(strings.Join(row, " | "))
		}
		sections = append(sections, DocumentSection{
			Label: fmt.Sprintf("slide %d table %d", slideNum, i+1),
			Text:  sb.String(),
		})
	}
	return sections
}

func anyNonEmpty(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return true
		}
	}
	return false
}
