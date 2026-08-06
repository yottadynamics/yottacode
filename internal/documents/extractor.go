// Package documents provides bounded, provenance-rich text extraction
// from local structured and markup files — CSV, TSV, JSON, JSONL, XML,
// and HTML — for the read_document agent tool. It is a pure content
// library: it knows nothing about cwd resolution or the credential-path
// denylist (that trust boundary lives at the agent-tool layer, same as
// read_file's ValidateReadPath), and takes an already-resolved,
// already-validated absolute path.
//
// Every extractor enforces its byte/row/char caps before or during
// parsing rather than reading the whole file into memory first, so a
// pathological input (huge file, one giant line with no row
// boundaries) can't exhaust memory. Every returned section carries a
// provenance label ("rows 1-40", "JSONL line 412", "heading: Results")
// so the model can cite an exact location instead of an offset into an
// opaque blob.
package documents

import "context"

const (
	// DefaultMaxBytes bounds how much of the source file any extractor
	// reads, regardless of how many rows/records that covers. This is
	// the hard ceiling against pathological input (a single line with
	// no row boundaries, for instance) where row/char caps alone
	// wouldn't stop unbounded buffering.
	DefaultMaxBytes int64 = 5 * 1024 * 1024 // 5 MiB

	// DefaultMaxRows bounds how many logical rows/records (CSV/TSV rows,
	// JSONL records) an extractor samples into the preview.
	DefaultMaxRows = 200

	// DefaultMaxChars bounds the total extracted text (JSON/XML/HTML
	// content preview) so a single document can't dominate the model's
	// context window.
	DefaultMaxChars = 20000

	// MaxAllowedBytes is the ceiling on a caller-supplied MaxBytes.
	// MaxBytes is the only cap that needs one: MaxRows and MaxChars are
	// transitively bounded by it (no extractor can sample more rows or
	// characters than the bytes it was allowed to read), so bounding
	// bytes bounds everything downstream.
	//
	// The ceiling exists mainly for the .json path, which is the one
	// extractor that buffers the whole allowance and decodes it into an
	// `any` tree — a representation several times larger than the source
	// text. The streaming extractors (CSV/TSV/JSONL/XML/HTML) grow only
	// linearly with the allowance. 32 MiB is ~6x the default, which
	// covers the realistic "my export is bigger than 5 MiB" case while
	// keeping even the amplified JSON worst case survivable on a dev
	// machine.
	MaxAllowedBytes int64 = 32 * 1024 * 1024 // 32 MiB
)

// ExtractRequest carries the local file to parse plus the caps that
// bound the work. Zero-value fields are filled with the Default*
// constants by withDefaults; callers only need to set what they want
// to override.
type ExtractRequest struct {
	// Path is an already-resolved, already-trust-validated absolute
	// path. Extractors do not re-validate it.
	Path string

	MaxBytes int64
	MaxChars int
	MaxRows  int

	// Offset is where the preview window starts, counted in whatever
	// unit that format's preview is made of: data rows for CSV/TSV,
	// records for JSONL, characters for the JSON/XML/HTML text preview.
	// One name rather than row_offset/char_offset because every result
	// labels the window it actually returned ("rows 201-400 of 5000"),
	// so the unit is unambiguous exactly where it's read.
	//
	// Skipping still requires streaming through what came before —
	// rows are variable-length and a quoted field may contain newlines,
	// so there is no seek. Skipped content is parsed and discarded
	// rather than retained, so paging costs time, not memory. It does
	// consume the MaxBytes budget, and a window that lies past the cap
	// says so rather than looking like the end of the file.
	Offset int

	// HasHeader overrides CSV/TSV header handling. Nil auto-detects
	// (see firstRowLooksLikeData); non-nil forces the answer, which is
	// the escape hatch for the cases the heuristic cannot get right —
	// a header of bare years, or a headerless file whose first row
	// happens to be all text. Ignored by non-tabular extractors.
	HasHeader *bool
}

func (r ExtractRequest) withDefaults() ExtractRequest {
	if r.MaxBytes <= 0 {
		r.MaxBytes = DefaultMaxBytes
	}
	if r.MaxBytes > MaxAllowedBytes {
		r.MaxBytes = MaxAllowedBytes
	}
	if r.Offset < 0 {
		r.Offset = 0
	}
	if r.MaxChars <= 0 {
		r.MaxChars = DefaultMaxChars
	}
	if r.MaxRows <= 0 {
		r.MaxRows = DefaultMaxRows
	}
	return r
}

// DocumentMetadata is the structure summary shown above the content
// preview — what the model needs to decide whether to page further
// without having read the content yet.
type DocumentMetadata struct {
	// Kind is the extractor's short format name: "csv", "tsv", "json",
	// "jsonl", "xml", or "html".
	Kind string

	// SizeBytes is the source file's size on disk (not the bounded
	// amount actually read).
	SizeBytes int64

	// Columns is the CSV/TSV header row, when the file has one. Empty
	// for other formats.
	Columns []string

	// RowCount is the number of logical rows/records the extractor
	// actually walked (which may be less than the file's true row
	// count when a cap stopped it early — see Warnings for that case).
	RowCount int

	// Shape is a free-form one-line structure summary: JSON top-level
	// keys/types, the XML root element name, or the HTML <title>.
	Shape string
}

// DocumentSection is one labeled chunk of extracted text. Label carries
// provenance so the model can point back at an exact location instead
// of an offset into an opaque blob.
type DocumentSection struct {
	Label string
	Text  string
}

// ExtractResult is what read_document hands back to the model.
type ExtractResult struct {
	Metadata DocumentMetadata
	Sections []DocumentSection

	// Warnings surfaces truncation and degraded-parse notices — e.g.
	// "stopped at the 5 MiB read cap after 40 rows" or "malformed row
	// 12 skipped". Never silent: a capped or partial read always shows
	// up here.
	Warnings []string
}

// Extractor converts one local document into a bounded ExtractResult.
type Extractor interface {
	// Match reports whether this extractor handles path, based on its
	// extension. The Phase A format set is self-describing by suffix,
	// so content sniffing isn't needed.
	Match(path string) bool

	Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error)
}
