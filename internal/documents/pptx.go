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
// zip+XML walk (no external binary), matching the "text only" tier of
// the pptx parse fallback chain in roadmap/document-generation.md.
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
		text, slideWarn := extractPptxSlideText(rc, budget)
		rc.Close()
		if slideWarn != "" {
			warnings = append(warnings, fmt.Sprintf("slide %d: %s", se.num, slideWarn))
		}
		budget -= int64(len(text))
		if budget < 0 {
			budget = 0
		}
		texts = append(texts, text)
	}

	sections, sectionWarnings := buildLabeledUnitSections(texts, req.Offset+1, req.MaxChars, "slide")
	warnings = append(warnings, sectionWarnings...)

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

// extractPptxSlideText walks one slide's XML, joining every <a:t> text
// run with a space. maxBytes caps this slide's share of the overall
// decompressed-size budget.
func extractPptxSlideText(r io.Reader, maxBytes int64) (string, string) {
	if maxBytes <= 0 {
		return "", "skipped: decompressed-size cap already exhausted by earlier slides"
	}
	dec := xml.NewDecoder(io.LimitReader(r, maxBytes))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.TrimSpace(b.String()), fmt.Sprintf("stopped parsing: %v", err)
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "t" {
			var s string
			if err := dec.DecodeElement(&s, &t); err == nil {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(s)
			}
		}
	}
	return strings.TrimSpace(b.String()), ""
}
