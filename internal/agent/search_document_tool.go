package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/documents"
	"github.com/yottadynamics/yottacode/internal/memory"
)

// Generous ceilings for the content search_document pulls in before
// chunking/scoring — well above read_document's small preview
// defaults (DefaultMaxChars=20000, DefaultMaxRows=200,
// DefaultMaxPages=50), since a search needs to see much more of a
// document than a preview does. MaxBytes is the real backstop
// (documents.MaxAllowedBytes, the same ceiling read_document already
// enforces); these are set high enough that MaxBytes is what actually
// bounds a large file, not these counts.
const (
	searchDocumentMaxChars = 2_000_000
	searchDocumentMaxRows  = 1_000_000
	searchDocumentMaxPages = 2000

	// searchDocumentChunkChars bounds a single scored unit. A
	// DocumentSection can be as large as an entire docx body, which
	// would let BM25 only say "this document matches" rather than
	// pointing at where — see chunkSections.
	searchDocumentChunkChars = 800
	// searchDocumentChunkOverlap keeps a hard split (a paragraph itself
	// longer than searchDocumentChunkChars) from cutting a match in
	// half at a chunk boundary.
	searchDocumentChunkOverlap = 100

	searchDocumentDefaultMaxResults = 10
)

// SearchDocumentTool does query-based retrieval over a document's
// extracted content, instead of read_document's blind offset/char
// paging — most useful for a large PDF/docx/xlsx where the caller
// doesn't know which page/section holds what it needs.
//
// Read-only, no approval — same trust posture as read_document, which
// it builds on: Execute constructs a ReadDocumentTool from the same
// fields to reuse its registry()/sandbox/pyhelper-script-resolver
// plumbing (legal since both types live in package agent), rather than
// duplicating that glue a third time (it's already duplicated once,
// between ReadDocumentTool and CreateDocumentTool).
type SearchDocumentTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string

	// Registry is the format dispatch table. Nil uses the production
	// registry (same as ReadDocumentTool); tests can inject a fake.
	Registry *documents.Registry

	// Sandbox is nil-safe: a nil Sandbox behaves exactly like
	// HostSandbox, mirroring ReadDocumentTool.Sandbox.
	Sandbox Sandbox

	// SubprocessFormatsEnabled gates .pdf specifically — see
	// ReadDocumentTool.SubprocessFormatsEnabled.
	SubprocessFormatsEnabled bool
}

// reader builds the ReadDocumentTool this tool reuses for path
// validation and format dispatch.
func (t *SearchDocumentTool) reader() *ReadDocumentTool {
	return &ReadDocumentTool{
		Cwd:                      t.Cwd,
		DenyReadPaths:            t.DenyReadPaths,
		Registry:                 t.Registry,
		Sandbox:                  t.Sandbox,
		SubprocessFormatsEnabled: t.SubprocessFormatsEnabled,
	}
}

func (t *SearchDocumentTool) Name() string { return "search_document" }

func (t *SearchDocumentTool) Description() string {
	return "Search for text within a CSV, TSV, JSON, JSONL, XML, HTML, PDF, xlsx, docx, or pptx file, returning ranked, scored, snippeted matches instead of a full preview. " +
		"Use this instead of paging read_document with offset when you don't know which page, sheet, or section of a large document holds what you need — " +
		"it extracts the same content read_document would and ranks it by relevance to your query (BM25 keyword ranking, not exact substring matching), " +
		"returning up to max_results matches each labeled with its location (e.g. 'page 4 (part 2)', 'Sheet1') and a short snippet. " +
		"Subject to the same PDF-availability and path-trust rules as read_document."
}

func (t *SearchDocumentTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or cwd-relative path to a .csv, .tsv, .json, .jsonl, .xml, .html, .htm, .pdf, .xlsx, .docx, or .pptx file",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (keywords, not a regex or exact phrase)",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum number of ranked matches to return (default %d)", searchDocumentDefaultMaxResults),
			},
			"ocr_lang": map[string]any{
				"type":        "string",
				"description": "PDF only: Tesseract language code for the OCR fallback tier (e.g. \"fra\", \"eng+fra\") — see read_document's ocr_lang. Only relevant when the PDF has no embedded text layer at all.",
			},
		},
		"required": []string{"path", "query"},
	}
}

func (t *SearchDocumentTool) RequiresApproval(string) bool { return false }
func (t *SearchDocumentTool) ParallelSafe(string) bool     { return true }

type searchDocumentArgs struct {
	Path       string `json:"path"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	OCRLang    string `json:"ocr_lang"`
}

func (t *SearchDocumentTool) PreviewCall(argsJSON string) string {
	var a searchDocumentArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("search_document(%s, %q)", a.Path, a.Query)
}

func (t *SearchDocumentTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a searchDocumentArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("search_document: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("search_document: path is required")
	}
	if a.Query == "" {
		return "", fmt.Errorf("search_document: query is required")
	}
	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = searchDocumentDefaultMaxResults
	}

	reader := t.reader()
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("search_document: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(p), ".pdf") && !t.SubprocessFormatsEnabled {
		return "", fmt.Errorf("search_document: PDF extraction is disabled in this configuration")
	}

	ex := reader.registry().Lookup(p)
	if ex == nil {
		return "", fmt.Errorf("search_document: %w", documents.ErrUnsupported)
	}

	res, err := ex.Extract(ctx, documents.ExtractRequest{
		Path:     p,
		MaxBytes: documents.MaxAllowedBytes,
		MaxChars: searchDocumentMaxChars,
		MaxRows:  searchDocumentMaxRows,
		MaxPages: searchDocumentMaxPages,
		OCRLang:  a.OCRLang,
	})
	if err != nil {
		return "", fmt.Errorf("search_document: %w", err)
	}

	entries := chunkSections(res.Sections, searchDocumentChunkChars, searchDocumentChunkOverlap)
	if len(entries) == 0 {
		return fmt.Sprintf("no matching text found for %q in %s (document has no extractable content)", a.Query, p), nil
	}

	cfg := config.RetrievalConfig{
		Enabled:  true,
		TopK:     maxResults,
		MinScore: memory.ExplicitSearchMinScore,
		Strategy: "bm25",
	}
	scored := memory.SelectWithEmbeddingsScored(ctx, entries, a.Query, cfg, nil)

	kept := make([]memory.Scored, 0, len(scored))
	for _, s := range scored {
		if memory.ExplicitSearchMatch(s.Entry, a.Query, s.Score) {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return fmt.Sprintf("no matching text found for %q in %s", a.Query, p), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "found %d matches for %q in %s:\n", len(kept), a.Query, p)
	for i, s := range kept {
		fmt.Fprintf(&b, "\n%d. %s (score=%.3f)\n", i+1, s.Entry.Name, s.Score)
		fmt.Fprintf(&b, "   %s\n", truncateRunes(strings.TrimSpace(s.Entry.Body), 300))
	}
	return b.String(), nil
}

// chunkSections splits every section's text into scorable units small
// enough to localize a match within a large section (a docx body, for
// instance, is returned as a single section covering the whole
// document) — splitting on paragraph (blank-line) boundaries, and hard
// splitting with a small overlap when a single paragraph itself
// exceeds maxChars, so no content is ever silently dropped. Pure
// function, no I/O — unit-testable with canned sections.
func chunkSections(sections []documents.DocumentSection, maxChars, overlap int) []memory.MemoryEntry {
	var entries []memory.MemoryEntry
	for _, sec := range sections {
		paras := splitParagraphs(sec.Text)
		var chunks []string
		for _, para := range paras {
			chunks = append(chunks, hardSplit(para, maxChars, overlap)...)
		}
		if len(chunks) == 0 {
			continue
		}
		for i, chunk := range chunks {
			label := sec.Label
			if len(chunks) > 1 {
				label = fmt.Sprintf("%s (part %d)", sec.Label, i+1)
			}
			entries = append(entries, memory.MemoryEntry{Name: label, Body: chunk})
		}
	}
	return entries
}

// splitParagraphs splits text on blank lines, dropping any paragraph
// that's empty after trimming.
func splitParagraphs(text string) []string {
	raw := strings.Split(text, "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// hardSplit breaks s into pieces of at most maxChars runes, each
// overlapping the previous by overlap runes, so a match spanning a
// split point is never fully lost from both sides. A paragraph already
// within maxChars is returned unchanged as a single piece.
func hardSplit(s string, maxChars, overlap int) []string {
	r := []rune(s)
	if len(r) <= maxChars {
		return []string{s}
	}
	var out []string
	step := maxChars - overlap
	if step <= 0 {
		step = maxChars
	}
	for start := 0; start < len(r); start += step {
		end := min(start+maxChars, len(r))
		out = append(out, string(r[start:end]))
		if end == len(r) {
			break
		}
	}
	return out
}

var (
	_ Tool             = (*SearchDocumentTool)(nil)
	_ ParallelSafeTool = (*SearchDocumentTool)(nil)
)
