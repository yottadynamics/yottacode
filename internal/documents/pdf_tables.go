package documents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScriptResolver resolves a PyHelperScript to an invocable filesystem
// path, given whatever Sandbox-awareness the caller has — mirrors
// CommandRunner's own caller-supplies-the-seam shape (see pdf.go's
// CommandRunner doc comment for the dependency-direction reasoning: this
// package defines the seam, internal/agent supplies the implementation
// via documents.ResolvePyHelperScript). A nil ScriptResolver means the
// optional tier that needs it is unavailable, same as a nil
// CommandRunner today.
type ScriptResolver func(script PyHelperScript) (string, error)

// pyTableExtractionOutput mirrors pyhelpers/extract_pdf_tables.py's
// stdout JSON shape exactly.
type pyTableExtractionOutput struct {
	Pages []struct {
		Page   int `json:"page"`
		Tables []struct {
			Rows [][]string `json:"rows"`
		} `json:"tables"`
	} `json:"pages"`
}

// pageTables is one PDF page's detected tables, 1-indexed to match
// pdftotext's own page numbering used elsewhere in this file. Each
// table is plain rows — pdfplumber doesn't distinguish a header row
// from body rows, so unlike Block's table type (built for generation,
// where the caller supplies an explicit header) this has none to
// preserve.
type pageTables struct {
	Page   int
	Tables [][][]string
}

// parsePythonTableJSON parses extract_pdf_tables.py's stdout. Pure
// function — no I/O, unit-testable with canned JSON.
func parsePythonTableJSON(out []byte) ([]pageTables, error) {
	var parsed pyTableExtractionOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("documents: parse table-extraction output: %w", err)
	}
	pages := make([]pageTables, 0, len(parsed.Pages))
	for _, p := range parsed.Pages {
		tables := make([][][]string, 0, len(p.Tables))
		for _, t := range p.Tables {
			tables = append(tables, t.Rows)
		}
		pages = append(pages, pageTables{Page: p.Page, Tables: tables})
	}
	return pages, nil
}

// buildTableSections renders every detected table across pages as
// labeled DocumentSections, one per table: "page N table M". Row cells
// are pipe-joined, matching xlsx_extract.go's own row-formatting
// convention so read_document's table output reads consistently across
// formats. Pages/tables with zero rows are skipped — pdfplumber can
// return an empty table for a false-positive detection, and an empty
// section would be noise, not signal.
func buildTableSections(pages []pageTables) []DocumentSection {
	var sections []DocumentSection
	for _, p := range pages {
		for i, rows := range p.Tables {
			if len(rows) == 0 {
				continue
			}
			var b strings.Builder
			for r, row := range rows {
				if r > 0 {
					b.WriteString("\n")
				}
				b.WriteString(strings.Join(row, " | "))
			}
			sections = append(sections, DocumentSection{
				Label: fmt.Sprintf("page %d table %d", p.Page, i+1),
				Text:  b.String(),
			})
		}
	}
	return sections
}

// buildTableExtractionCommand builds the shell command line
// extractTables runs. Pure function (no I/O) so its output shape is
// unit-testable without python installed, mirroring
// internal/agent's buildPandocCommand.
func buildTableExtractionCommand(scriptPath, pdfPath string, startPage, endPage int) string {
	return fmt.Sprintf("python3 %s %s --start %d --end %d",
		shellQuote(scriptPath), shellQuote(pdfPath), startPage, endPage)
}
