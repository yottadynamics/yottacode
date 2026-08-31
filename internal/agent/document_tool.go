package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// ReadDocumentTool extracts bounded, provenance-labeled text from CSV,
// TSV, JSON, JSONL, XML, and HTML files — a structured alternative to
// read_file for these formats. Where read_file returns a raw cat -n
// dump (which shears a CSV field's embedded newline into a bogus extra
// row, and dumps HTML/XML markup noise verbatim), read_document parses
// the format properly and returns a structure summary plus a bounded,
// row/record-aligned preview.
//
// Read-only, no approval — same trust posture as read_file. Path
// validation and the credential-path denylist happen here, at the
// agent-tool boundary; internal/documents itself is a pure content
// library with no notion of cwd or trust.
type ReadDocumentTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string

	// Registry is the format dispatch table. Nil uses the production
	// registry (CSV/TSV/JSON/JSONL/XML/HTML, plus PDF when Sandbox lets
	// pdftotext/pdfinfo run); tests can inject a fake.
	Registry *documents.Registry

	// Sandbox is nil-safe: a nil Sandbox behaves exactly like HostSandbox,
	// mirroring RunBashTool.Sandbox and CreateDocumentTool.Sandbox. Only
	// consulted for PDF and docx's optional pandoc tier — every other
	// format (and docx's own native fallback) is pure Go and never
	// shells out.
	Sandbox Sandbox

	// SubprocessFormatsEnabled gates .pdf specifically — the one format
	// with no native fallback: PDFExtractor always needs pdftotext/
	// pdfinfo reachable through the sandbox. Every other format
	// (csv/tsv/json/jsonl/xml/html/xlsx/docx/pptx) is pure Go and
	// unaffected; docx's optional pandoc tier degrades silently to its
	// native tier when pandoc is unavailable, so it isn't gated by this
	// field either. document_ingestion graduated to GA (see
	// internal/experimental/features.go), so every real caller wires
	// this true unconditionally; the field stays as a structural on/off
	// switch for a caller that wants PDF excluded for reasons of its
	// own. A missing pdftotext/pdfinfo binary is reported separately by
	// PDFExtractor, not by this field.
	SubprocessFormatsEnabled bool
}

func (t *ReadDocumentTool) sandbox() Sandbox {
	if t.Sandbox != nil {
		return t.Sandbox
	}
	return HostSandbox{}
}

func (t *ReadDocumentTool) registry() *documents.Registry {
	if t.Registry != nil {
		return t.Registry
	}
	reg := documents.NewRegistryWithDocxTier(t.runSandboxCommand)
	reg.Register(&documents.PDFExtractor{Run: t.runSandboxCommand, ResolveScript: t.resolvePyHelperScript})
	return reg
}

// resolvePyHelperScript implements documents.ScriptResolver, routing
// through the same Sandbox-awareness runSandboxCommand already has: a
// podman-backed sandbox already has the driver scripts baked into its
// image by infra/documents.Containerfile, so it gets the fixed in-image
// path; everything else (HostSandbox, or any other future backend)
// materializes the embedded script under pyHelperCacheDir.
func (t *ReadDocumentTool) resolvePyHelperScript(script documents.PyHelperScript) (string, error) {
	cacheDir, err := pyHelperCacheDir()
	if err != nil {
		return "", err
	}
	isPodman := isPodmanSandboxLabel(LabelForProfile(t.sandbox(), SandboxProfileDocuments))
	return documents.ResolvePyHelperScript(script, isPodman, cacheDir)
}

// runSandboxCommand implements documents.CommandRunner by routing through
// the same Sandbox seam RunBashTool/CreateDocumentTool use — the whole
// point being that a podman-backed sandbox has its own PATH and
// filesystem view, independent of the host's, so PDF/docx parsing is
// sandboxed exactly like docx/pdf generation already is. Shared by both
// PDFExtractor and DocxExtractor's optional pandoc tier — one
// Sandbox-routing helper, not a copy per extractor.
func (t *ReadDocumentTool) runSandboxCommand(ctx context.Context, command string) ([]byte, []byte, error) {
	c := CommandInProfile(ctx, t.sandbox(), SandboxProfileDocuments, command, t.Cwd.Get())
	var stdout, stderr bytes.Buffer
	c.Stdout = &cappedWriter{buf: &stdout}
	c.Stderr = &cappedWriter{buf: &stderr}
	err := c.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (t *ReadDocumentTool) Name() string { return "read_document" }

func (t *ReadDocumentTool) Description() string {
	return "Extract bounded, structured text from a CSV, TSV, JSON, JSONL, XML, HTML, PDF, xlsx, docx, or pptx file. " +
		"Returns a structure summary (columns, JSON shape, XML/HTML title and headings, PDF page count, xlsx sheet names, docx paragraph/heading counts, pptx slide count) plus a capped content preview, " +
		"with every truncation reported explicitly rather than silently cut. " +
		"PDF extraction runs pdftotext/pdfinfo, which must be reachable through the documents sandbox profile " +
		"(installed on the host when no sandbox is configured, or present in [sandbox].documents_image); " +
		"a missing binary returns an actionable error naming exactly where it looked, rather than failing " +
		"silently. An encrypted PDF is reported as a warning rather than an error, since it's still a valid " +
		"result to hand back — detected directly from pdfinfo's own Encrypted field when it reports one, " +
		"falling back to a pdftotext-failure heuristic otherwise. The PDF's own Title/Author/CreationDate " +
		"metadata, when present, are returned as title/author/created lines above the content preview. " +
		"When python3+pdfplumber are also reachable through that " +
		"same documents profile or host PATH, detected tables are returned as additional 'page N table M' " +
		"sections with pipe-joined rows, and detected images as 'page N image M' sections (size in inches, plus " +
		"source pixel resolution when known — never image bytes); this tier is best-effort and silently absent " +
		"(not an error or warning) when python3/pdfplumber aren't available, or a page genuinely has neither. " +
		"A PDF page with no embedded text layer falls back to OCR when python3+pytesseract+pdf2image+tesseract-ocr are reachable the same way, " +
		"returning recognized text as additional 'page N (ocr)' sections with a warning that the text may contain recognition errors; use ocr_lang if the PDF isn't in English. " +
		"xlsx/docx/pptx are parsed natively (no external tools): xlsx returns one section per sheet, plus a 'sheet X formulas' section (only for cells actually shown in the preview window) since the sheet's own text always shows a cell's cached computed value, never its formula, a 'sheet X merged cells' section when any exist, and a 'sheet X image <cell>-N' section per embedded picture (type/pixel size/alt text). " +
		"docx returns the document body as one section with headings rendered as '# '-prefixed lines, plus a 'document image N' section per inline picture (file name/size/alt-text metadata), present whichever text tier served the body. " +
		"pptx returns one section per slide in slide order, plus a 'slide N table M' section for each detected table (pipe-joined rows) and a 'slide N image M' section per picture with file name/size/alt-text metadata. " +
		"All picture/image sections across formats are metadata only — never image bytes; read the media file directly via read_file if you need the actual image. " +
		"Use this when you need to ANALYZE the data — read_file's raw line-based view shears a CSV field's " +
		"embedded newline into a bogus extra row and returns HTML/XML markup noise verbatim. " +
		"Keep using read_file when you intend to EDIT the file: only read_file's cat -n output feeds " +
		"exact-string edit_file and anchored edit_anchored, and this tool's reformatted preview cannot be used that way. " +
		"That matters most for .json/.xml/.html, which are often source files you are editing rather than data you are reading. " +
		"Handles real-world export quirks: a UTF-8 BOM is stripped, a semicolon/tab/pipe-delimited .csv is detected rather than collapsed into one column, " +
		"and a file with no header row is recognized instead of losing its first record to the column names. " +
		"Use max_rows/max_chars to resize the preview, max_pages for PDF pages or pptx slides, offset to page past what you have already seen, " +
		"max_bytes to read further into a file that hit the byte cap, " +
		"and has_header to override header detection when it guessed wrong."
}

func (t *ReadDocumentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or cwd-relative path to a .csv, .tsv, .json, .jsonl, .xml, .html, .htm, .pdf, .xlsx, .docx, or .pptx file",
			},
			"max_rows": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum CSV/TSV/JSONL rows or records to sample (default %d).", documents.DefaultMaxRows),
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum characters of extracted text to return (default %d).", documents.DefaultMaxChars),
			},
			"max_pages": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("PDF or pptx only: maximum pages/slides to read text from (default %d).", documents.DefaultMaxPages),
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Where the preview window starts (default 0). Counted in data rows for CSV/TSV, records for JSONL, and characters for JSON/XML/HTML. Page through a large file by repeating the call with offset advanced by the number of rows/records you got back; every result labels the window it returned.",
			},
			"has_header": map[string]any{
				"type":        "boolean",
				"description": "CSV/TSV only: whether the first row holds column names. Omit to auto-detect — a first row containing a number is read as data, not a header. Set it explicitly when the result's header looks wrong.",
			},
			"max_bytes": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"Maximum bytes to read from the source file (default %d, ceiling %d). Raise this when a result warns that the file exceeded the byte cap.",
					documents.DefaultMaxBytes, documents.MaxAllowedBytes),
			},
			"ocr_lang": map[string]any{
				"type":        "string",
				"description": "PDF only: Tesseract language code for the OCR fallback tier (e.g. \"fra\", \"deu\"), or several joined with \"+\" for mixed-language text (e.g. \"eng+fra\"). Omit for the default, English. Only used when the PDF has no embedded text layer at all; ignored otherwise. The requested language pack must be installed wherever pdftotext/tesseract run — an unavailable one degrades silently like any other missing OCR dependency.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadDocumentTool) RequiresApproval(string) bool { return false }
func (t *ReadDocumentTool) ParallelSafe(string) bool     { return true }

type readDocumentArgs struct {
	Path     string `json:"path"`
	MaxRows  int    `json:"max_rows"`
	MaxChars int    `json:"max_chars"`
	MaxBytes int64  `json:"max_bytes"`
	MaxPages int    `json:"max_pages"`
	Offset   int    `json:"offset"`
	OCRLang  string `json:"ocr_lang"`

	// HasHeader is a pointer so an omitted key stays distinguishable
	// from an explicit false: absent means "auto-detect", which is a
	// different instruction than "this file has no header".
	HasHeader *bool `json:"has_header"`
}

func (t *ReadDocumentTool) PreviewCall(argsJSON string) string {
	var a readDocumentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("read_document(%s)", a.Path)
}

func (t *ReadDocumentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a readDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("read_document: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("read_document: path is required")
	}

	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("read_document: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(p), ".pdf") && !t.SubprocessFormatsEnabled {
		return "", fmt.Errorf("read_document: PDF extraction is disabled in this configuration")
	}

	ex := t.registry().Lookup(p)
	if ex == nil {
		return "", fmt.Errorf("read_document: %w", documents.ErrUnsupported)
	}

	res, err := ex.Extract(ctx, documents.ExtractRequest{
		Path:      p,
		MaxRows:   a.MaxRows,
		MaxChars:  a.MaxChars,
		MaxBytes:  a.MaxBytes,
		MaxPages:  a.MaxPages,
		Offset:    a.Offset,
		HasHeader: a.HasHeader,
		OCRLang:   a.OCRLang,
	})
	if err != nil {
		return "", fmt.Errorf("read_document: %w", err)
	}

	// withDefaults silently clamps an over-ceiling MaxBytes, which would
	// otherwise leave the model believing it got the allowance it asked
	// for. Say so — the whole point of this tool is that limits are
	// stated, not inferred from a short result.
	if a.MaxBytes > documents.MaxAllowedBytes {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"requested max_bytes %d exceeds the %d-byte ceiling; clamped to the ceiling",
			a.MaxBytes, documents.MaxAllowedBytes))
	}

	return formatDocumentResult(p, res), nil
}

// formatDocumentResult renders an ExtractResult as the tool's text
// output: a metadata header, each labeled section, then any warnings
// last so truncation/degradation is never buried above the fold.
func formatDocumentResult(path string, res documents.ExtractResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %d bytes)\n", path, res.Metadata.Kind, res.Metadata.SizeBytes)
	if res.Metadata.Shape != "" {
		fmt.Fprintf(&b, "shape: %s\n", res.Metadata.Shape)
	}
	if res.Metadata.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", res.Metadata.Title)
	}
	if res.Metadata.Author != "" {
		fmt.Fprintf(&b, "author: %s\n", res.Metadata.Author)
	}
	if res.Metadata.CreationDate != "" {
		fmt.Fprintf(&b, "created: %s\n", res.Metadata.CreationDate)
	}
	if len(res.Metadata.Columns) > 0 {
		fmt.Fprintf(&b, "columns: %s\n", strings.Join(res.Metadata.Columns, ", "))
	}
	if res.Metadata.RowCount > 0 {
		fmt.Fprintf(&b, "rows: %d\n", res.Metadata.RowCount)
	}

	for _, sec := range res.Sections {
		fmt.Fprintf(&b, "\n[%s]\n%s\n", sec.Label, sec.Text)
	}

	if len(res.Warnings) > 0 {
		b.WriteString("\nwarnings:\n")
		for _, w := range res.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
