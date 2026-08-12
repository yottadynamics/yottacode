package documents

import "fmt"

// Registry dispatches to the first Extractor whose Match reports true,
// in registration order.
type Registry struct {
	extractors []Extractor
}

// NewRegistry returns a Registry pre-loaded with every extractor that
// needs no subprocess: the Phase A formats (CSV, TSV, JSON, JSONL, XML,
// HTML) plus the native zip/xlsx-based Phase C formats (xlsx via
// excelize, docx and pptx via zip+XML). PDF is deliberately NOT
// registered here — it needs a CommandRunner (pdftotext/pdfinfo), which
// only the agent-tool layer can supply (see PDFExtractor's doc comment);
// callers that want PDF support register it themselves after NewRegistry.
// docx is included with a nil Run (native tier only, always fully
// functional) — unlike PDF, docx doesn't need a CommandRunner to be
// useful, so a bare NewRegistry() call still gets working docx support.
// Callers that also want docx's optional pandoc tier use
// NewRegistryWithDocxTier instead.
func NewRegistry() *Registry {
	return NewRegistryWithDocxTier(nil)
}

// NewRegistryWithDocxTier is NewRegistry, but wires run into
// DocxExtractor.Run so its optional pandoc tier is available. A separate
// constructor rather than a second Register call after NewRegistry,
// because Registry.Lookup matches in registration order: a second
// DocxExtractor registered afterward would never be reached — the first
// (nil-Run, native-only) one registered by NewRegistry would always win.
func NewRegistryWithDocxTier(run CommandRunner) *Registry {
	r := &Registry{}
	r.Register(NewCSVExtractor())
	r.Register(NewTSVExtractor())
	r.Register(&JSONExtractor{})
	r.Register(&XMLExtractor{})
	r.Register(&HTMLExtractor{})
	r.Register(&XLSXExtractor{})
	r.Register(&DocxExtractor{Run: run})
	r.Register(&PptxExtractor{})
	return r
}

// Register adds e to the dispatch list. Later registrations are tried
// after earlier ones, so more specific extractors should be registered
// first if extension ranges ever overlap.
func (r *Registry) Register(e Extractor) {
	r.extractors = append(r.extractors, e)
}

// Lookup returns the first extractor whose Match reports true for path,
// or nil if no registered format recognizes it.
func (r *Registry) Lookup(path string) Extractor {
	for _, e := range r.extractors {
		if e.Match(path) {
			return e
		}
	}
	return nil
}

// ErrUnsupported reports that no registered extractor recognizes the
// given path's extension. Kept here so every caller (the read_document
// tool today, any future caller later) shares one message.
var ErrUnsupported = fmt.Errorf("unsupported document type — read_document handles .csv, .tsv, .json, .jsonl, .xml, .html, .xlsx, .docx, .pptx, and (when a command sandbox can reach pdftotext) .pdf")
