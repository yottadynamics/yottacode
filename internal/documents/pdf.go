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
	// for tabular PDF content with no new dependency or architecture —
	// see roadmap/document-generation.md's Docling-deferral precedent
	// for why a pdfplumber-based structured-table tier stays deferred
	// instead (it would need read_document to write a temp script file,
	// a new side effect for what's otherwise a pure-read, no-approval
	// tool).
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

	if totalPages > 0 && endPage < totalPages {
		warnings = append(warnings, fmt.Sprintf("showing pages %d-%d of %d (page cap)", startPage, endPage, totalPages))
	}
	if allPagesBlank(pages) {
		warnings = append(warnings, "extracted text is empty across every returned page; this PDF may be scanned/image-only (OCR is not supported)")
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

func allPagesBlank(pages []string) bool {
	for _, p := range pages {
		if strings.TrimSpace(p) != "" {
			return false
		}
	}
	return len(pages) > 0
}

// shellQuote wraps s in POSIX single quotes, escaping any embedded single
// quote — the same technique internal/agent's shellQuoteSingle uses, kept
// as a small local duplicate rather than shared across packages since
// internal/documents cannot import internal/agent (see the package doc's
// dependency-direction note).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
