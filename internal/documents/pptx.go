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
// roadmap/document-generation.md, additionally augmented with real
// DrawingML table structure (see extractPptxSlideContent) so a table's
// cell text comes back as clean rows, not just run together with every
// other word on the slide.
type PptxExtractor struct{}

func (e *PptxExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pptx")
}

var pptxSlideNameRE = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

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
		text, tables, slideWarn := extractPptxSlideContent(rc, budget)
		rc.Close()
		if slideWarn != "" {
			warnings = append(warnings, fmt.Sprintf("slide %d: %s", se.num, slideWarn))
		}
		budget -= int64(len(text))
		if budget < 0 {
			budget = 0
		}
		texts = append(texts, text)
		tableSections = append(tableSections, buildPptxTableSections(se.num, tables)...)
	}

	sections, sectionWarnings := buildLabeledUnitSections(texts, req.Offset+1, req.MaxChars, "slide")
	warnings = append(warnings, sectionWarnings...)
	// Additive, same as PDF's table tier: a slide's table cell text
	// already appears in its plain "slide N" section above (run
	// together with every other word, same fidelity pdftotext's own
	// plain-text tier has for a PDF table); this adds a second, cleanly
	// structured rendering alongside it rather than replacing anything.
	sections = append(sections, tableSections...)

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
// same reasoning PDF table extraction's own pageTables type already
// documents for pdfplumber's output.
type pptxTable struct {
	Rows [][]string
}

// extractPptxSlideContent walks one slide's XML once, returning both
// its flat text (every <a:t> run joined with a space, exactly what
// extractPptxSlideText always returned — a table's cell text is still
// included here, run together with everything else, unchanged) and
// any DrawingML tables found on the slide as clean rows, tracked by
// table depth alongside the same walk. maxBytes caps this slide's
// share of the overall decompressed-size budget.
//
// A table nested inside another table's cell (vanishingly rare in
// real decks) is not tracked as its own separate table — its text
// still reaches the flat output and the outer cell's text, just not a
// distinct nested pptxTable; documented trade-off, not a bug to fix
// later, mirroring extract_pdf_tables.py's own documented shortcuts.
func extractPptxSlideContent(r io.Reader, maxBytes int64) (text string, tables []pptxTable, warn string) {
	if maxBytes <= 0 {
		return "", nil, "skipped: decompressed-size cap already exhausted by earlier slides"
	}
	dec := xml.NewDecoder(io.LimitReader(r, maxBytes))
	var b strings.Builder

	tableDepth := 0
	var curTable *pptxTable
	var curRow []string
	var curCell strings.Builder
	inCell := false

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

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.TrimSpace(b.String()), tables, fmt.Sprintf("stopped parsing: %v", err)
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
			}
		}
	}
	return strings.TrimSpace(b.String()), tables, ""
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
