package documents

import "fmt"

// Registry dispatches to the first Extractor whose Match reports true,
// in registration order.
type Registry struct {
	extractors []Extractor
}

// NewRegistry returns a Registry pre-loaded with the Phase A extractors:
// CSV, TSV, JSON, JSONL, XML, and HTML.
func NewRegistry() *Registry {
	r := &Registry{}
	r.Register(NewCSVExtractor())
	r.Register(NewTSVExtractor())
	r.Register(&JSONExtractor{})
	r.Register(&XMLExtractor{})
	r.Register(&HTMLExtractor{})
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
var ErrUnsupported = fmt.Errorf("unsupported document type — read_document handles .csv, .tsv, .json, .jsonl, .xml, .html")
