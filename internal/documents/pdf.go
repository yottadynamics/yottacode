package documents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CommandRunner runs one command through whatever execution seam the
// caller has (host PATH or a podman sandbox) and returns its captured
// stdout/stderr. This is the seam that lets PDFExtractor stay a pure
// content library — see the package doc — while still shelling out to
// pdftotext/pdfinfo: internal/documents defines the seam, internal/agent
// (which owns the Sandbox interface) supplies the implementation, the
// same dependency direction generation already uses for pandoc. A nil
// CommandRunner means PDF extraction is unavailable.
type CommandRunner func(ctx context.Context, command string) (stdout, stderr []byte, err error)

// PDFExtractor extracts text from PDF files via pdftotext/pdfinfo
// (poppler-utils). Unlike every other extractor in this package, it needs
// a subprocess — Run must be supplied by the caller (see CommandRunner).
type PDFExtractor struct {
	Run CommandRunner

	// ResolveScript enables the optional pdfplumber-backed table-
	// extraction tier (see extractTables) and the optional
	// pytesseract-backed OCR tier (see extractOCR) — both are named by
	// PyHelperScript, so one resolver serves either. Nil-safe: a nil
	// resolver just skips both tiers, same as a nil Run skips
	// extraction entirely — the primary pdftotext-based text extraction
	// never depends on this field.
	ResolveScript ScriptResolver
}

func (e *PDFExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pdf")
}

func (e *PDFExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	if e.Run == nil {
		return ExtractResult{}, errors.New("documents: pdf extraction unavailable: pdftotext must be reachable through the active command sandbox")
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	if info.Size() > req.MaxBytes {
		return ExtractResult{}, fmt.Errorf("documents: pdf %q (%d bytes) exceeds the %d-byte read cap", req.Path, info.Size(), req.MaxBytes)
	}

	var warnings []string
	totalPages := 0
	if out, _, err := e.Run(ctx, "pdfinfo "+shellQuote(req.Path)); err == nil {
		totalPages = parsePDFInfoPages(string(out))
	} else {
		warnings = append(warnings, "could not determine the PDF's total page count (pdfinfo unavailable or failed); page-limit truncation can't be confirmed against the true total")
	}

	if totalPages > 0 && req.Offset >= totalPages {
		return ExtractResult{
			Metadata: DocumentMetadata{Kind: "pdf", SizeBytes: info.Size(), Shape: fmt.Sprintf("%d pages", totalPages)},
			Warnings: append(warnings, fmt.Sprintf("offset %d is past the last page (%d total)", req.Offset, totalPages)),
		}, nil
	}

	startPage := req.Offset + 1
	endPage := req.Offset + req.MaxPages
	if totalPages > 0 && endPage > totalPages {
		endPage = totalPages
	}

	// -layout preserves column/table alignment as spaced plain text
	// instead of poppler's default reading-order reflow, which collapses
	// a table's columns into one run-together line. Real fidelity gain
	// for tabular PDF content with no new dependency or architecture.
	// The optional pdfplumber-backed tier below goes further (real
	// structured rows/columns, not just preserved alignment) when
	// ResolveScript is wired.
	cmd := fmt.Sprintf("pdftotext -layout -f %d -l %d %s -", startPage, endPage, shellQuote(req.Path))
	out, stderrOut, err := e.Run(ctx, cmd)
	if err != nil {
		if looksEncrypted(string(stderrOut)) {
			return ExtractResult{
				Metadata: DocumentMetadata{Kind: "pdf", SizeBytes: info.Size(), Shape: shapeFromTotalPages(totalPages)},
				Warnings: append(warnings, "this PDF appears to be encrypted/password-protected; no text could be extracted"),
			}, nil
		}
		return ExtractResult{}, fmt.Errorf("documents: pdftotext failed: %w (%s)", err, strings.TrimSpace(string(stderrOut)))
	}

	pages := splitPDFPages(string(out))
	sections, sectionsWarnings := buildLabeledUnitSections(pages, startPage, req.MaxChars, "page")
	warnings = append(warnings, sectionsWarnings...)

	// Best-effort, additive only: a missing script/python/pdfplumber, or
	// a page with genuinely no tables, must never turn a working
	// plain-text result into an error or even a warning — this tier is
	// pure upside when it works and silent when it doesn't.
	if tableSections := e.extractTables(ctx, req.Path, startPage, endPage); len(tableSections) > 0 {
		sections = append(sections, tableSections...)
	}

	if totalPages > 0 && endPage < totalPages {
		warnings = append(warnings, fmt.Sprintf("showing pages %d-%d of %d (page cap)", startPage, endPage, totalPages))
	}
	if blankRanges := blankPageRanges(pages); len(blankRanges) > 0 {
		// Best-effort OCR fallback, tried per contiguous run of blank
		// pages rather than only when the whole requested window is
		// blank — so a partially-scanned document (some pages with a
		// real text layer, some without) still gets OCR on the pages
		// that actually need it, without paying OCR's real cost on
		// pages that already have good text. Same silent-absent
		// contract as the table tier: a missing script/python/tesseract
		// never turns this into an error, only a less specific warning.
		totalBlank := 0
		var ocrSections []DocumentSection
		for _, r := range blankRanges {
			totalBlank += r.endIdx - r.startIdx + 1
			absStart := startPage + r.startIdx
			absEnd := startPage + r.endIdx
			ocrSections = append(ocrSections, e.extractOCR(ctx, req.Path, absStart, absEnd, req.OCRLang)...)
		}
		if len(ocrSections) > 0 {
			sections = append(sections, ocrSections...)
			if len(ocrSections) == totalBlank {
				warnings = append(warnings, fmt.Sprintf("primary text extraction found no embedded text layer on %d page(s); text below was recovered via OCR (tesseract) and may contain recognition errors", totalBlank))
			} else {
				warnings = append(warnings, fmt.Sprintf("primary text extraction found no embedded text layer on %d page(s); OCR (tesseract) recovered text for %d of them (may contain recognition errors) — the rest remain blank", totalBlank, len(ocrSections)))
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("extracted text is empty on %d of %d returned page(s); this PDF may be partially or fully scanned/image-only (install tesseract-ocr, pytesseract, and pdf2image for an OCR fallback, or point [sandbox].documents_image at an image that includes them)", totalBlank, len(pages)))
		}
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "pdf",
			SizeBytes: info.Size(),
			Shape:     shapeFromTotalPages(totalPages),
		},
		Sections: sections,
		Warnings: warnings,
	}, nil
}

func shapeFromTotalPages(totalPages int) string {
	if totalPages <= 0 {
		return ""
	}
	return fmt.Sprintf("%d pages", totalPages)
}

// pdfInfoPagesRE matches pdfinfo's plain-text "Pages:          12" line.
var pdfInfoPagesRE = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)

func parsePDFInfoPages(out string) int {
	m := pdfInfoPagesRE.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// looksEncrypted is a best-effort heuristic over poppler's own stderr
// text — there is no structured error type to check, and the exact
// wording isn't guaranteed stable across poppler versions.
func looksEncrypted(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "incorrect password") ||
		strings.Contains(strings.ToLower(stderr), "encrypted")
}

// splitPDFPages splits pdftotext's output on the form-feed character it
// emits between pages. pdftotext also emits a trailing form feed after
// the last page, which would otherwise produce a spurious empty final
// page; that trailing empty element is dropped.
func splitPDFPages(out string) []string {
	pages := strings.Split(out, "\f")
	if len(pages) > 0 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	return pages
}

// blankPageRange is one contiguous run of blank pages, as 0-indexed
// positions into the pages slice Extract already built (not absolute
// PDF page numbers — the caller adds startPage to convert).
type blankPageRange struct{ startIdx, endIdx int }

// blankPageRanges finds every contiguous run of blank pages in pages,
// in order. A "fully scanned" document (every page blank) collapses to
// exactly one range spanning the whole slice — the same span the OCR
// tier used to be triggered over before per-page detection, so that
// case's behavior is unchanged. A partially-scanned document yields
// one range per contiguous blank run, letting the caller OCR only the
// pages that actually need it.
func blankPageRanges(pages []string) []blankPageRange {
	var ranges []blankPageRange
	inRange := false
	var cur blankPageRange
	for i, p := range pages {
		if strings.TrimSpace(p) == "" {
			if !inRange {
				cur = blankPageRange{startIdx: i, endIdx: i}
				inRange = true
			} else {
				cur.endIdx = i
			}
		} else if inRange {
			ranges = append(ranges, cur)
			inRange = false
		}
	}
	if inRange {
		ranges = append(ranges, cur)
	}
	return ranges
}

// shellQuote wraps s in POSIX single quotes, escaping any embedded single
// quote — the same technique internal/agent's shellQuoteSingle uses, kept
// as a small local duplicate rather than shared across packages since
// internal/documents cannot import internal/agent (see the package doc's
// dependency-direction note).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// extractTables runs the optional pdfplumber-backed table-extraction
// tier over path's startPage-endPage range and returns rendered
// DocumentSections — nil on any failure (no ResolveScript/Run wired,
// script unresolvable, python3/pdfplumber missing, malformed output) so
// the caller can treat this purely additively. See buildTableSections'
// doc comment for the section shape and buildTableExtractionCommand for
// the exact command line.
func (e *PDFExtractor) extractTables(ctx context.Context, path string, startPage, endPage int) []DocumentSection {
	if e.Run == nil || e.ResolveScript == nil {
		return nil
	}
	scriptPath, err := e.ResolveScript(ScriptExtractPDFTables)
	if err != nil {
		return nil
	}
	cmd := buildTableExtractionCommand(scriptPath, path, startPage, endPage)
	out, _, err := e.Run(ctx, cmd)
	if err != nil {
		return nil
	}
	pages, err := parsePythonTableJSON(out)
	if err != nil {
		return nil
	}
	return buildTableSections(pages)
}

// extractOCR runs the optional pytesseract-backed OCR tier over path's
// startPage-endPage range and returns rendered DocumentSections — nil
// on any failure (no ResolveScript/Run wired, script unresolvable,
// python3/pytesseract/pdf2image/tesseract missing, malformed output,
// or an OCRLang naming a language pack that isn't installed) so the
// caller can treat this purely additively, the same contract
// extractTables already has. See buildOCRSections' doc comment for the
// section shape and buildPDFOCRCommand for the exact command line.
func (e *PDFExtractor) extractOCR(ctx context.Context, path string, startPage, endPage int, lang string) []DocumentSection {
	if e.Run == nil || e.ResolveScript == nil {
		return nil
	}
	scriptPath, err := e.ResolveScript(ScriptExtractPDFOCR)
	if err != nil {
		return nil
	}
	cmd := buildPDFOCRCommand(scriptPath, path, startPage, endPage, lang)
	out, _, err := e.Run(ctx, cmd)
	if err != nil {
		return nil
	}
	pages, err := parsePythonOCRJSON(out)
	if err != nil {
		return nil
	}
	return buildOCRSections(pages)
}
