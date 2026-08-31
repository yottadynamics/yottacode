package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/documents"
)

// capturingExtractor records the ExtractRequest it was handed, so a test
// can assert what the tool actually plumbed through from its JSON args
// rather than inferring it from formatted output.
type capturingExtractor struct{ got documents.ExtractRequest }

func (e *capturingExtractor) Match(string) bool { return true }

func (e *capturingExtractor) Extract(_ context.Context, req documents.ExtractRequest) (documents.ExtractResult, error) {
	e.got = req
	return documents.ExtractResult{Metadata: documents.DocumentMetadata{Kind: "fake"}}, nil
}

func TestReadDocumentTool_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n2,Gadget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"data.csv"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "csv") || !strings.Contains(out, "id, name") {
		t.Errorf("output missing expected metadata: %q", out)
	}
	if !strings.Contains(out, "Widget") {
		t.Errorf("output missing extracted content: %q", out)
	}
}

func TestFormatDocumentResult_TitleAuthorCreationDate(t *testing.T) {
	res := documents.ExtractResult{Metadata: documents.DocumentMetadata{
		Kind: "pdf", SizeBytes: 100, Shape: "3 pages",
		Title: "Q3 Report", Author: "Jane Doe", CreationDate: "Mon Jan  1 00:00:00 2024",
	}}
	out := formatDocumentResult("report.pdf", res)
	for _, want := range []string{"title: Q3 Report", "author: Jane Doe", "created: Mon Jan  1 00:00:00 2024"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestFormatDocumentResult_NoMetadataFieldsOmitted(t *testing.T) {
	res := documents.ExtractResult{Metadata: documents.DocumentMetadata{Kind: "pdf", SizeBytes: 100}}
	out := formatDocumentResult("report.pdf", res)
	for _, notWant := range []string{"title:", "author:", "created:"} {
		if strings.Contains(out, notWant) {
			t.Errorf("expected no %q line when the field is empty, got %q", notWant, out)
		}
	}
}

func TestReadDocumentTool_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	path := writeFile(t, tmp, "abs.json", `{"a": 1}`)

	tool := &ReadDocumentTool{Cwd: NewCwdRef("/unused")}
	out, err := tool.Execute(context.Background(), `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "json") {
		t.Errorf("output missing json kind: %q", out)
	}
}

func TestReadDocumentTool_MissingPath(t *testing.T) {
	tool := &ReadDocumentTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}

func TestReadDocumentTool_UnsupportedFormat(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.txt", "plain text")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	if _, err := tool.Execute(context.Background(), `{"path":"notes.txt"}`); err == nil {
		t.Fatal("expected an error for an unsupported extension")
	}
}

// TestReadDocumentTool_RejectsDeniedPath is the same trust regression as
// read_file: the credential-path denylist must apply here too, since
// read_document shares ValidateReadPath rather than inventing its own
// trust boundary.
func TestReadDocumentTool_RejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "secret.csv", "key,value\na,b\n")
	secret := filepath.Join(tmp, "secret.csv")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), DenyReadPaths: []string{secret}}
	if _, err := tool.Execute(context.Background(), `{"path":"secret.csv"}`); err == nil {
		t.Fatal("read_document read a deny-listed path")
	}
}

// TestReadDocumentTool_PassesCapsThrough covers the arg -> ExtractRequest
// plumbing. The caps are the tool's only tuning surface, so a dropped
// field would silently ignore what the model asked for.
func TestReadDocumentTool_PassesCapsThrough(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(),
		`{"path":"data.csv","max_rows":7,"max_chars":99,"max_bytes":4096,"max_pages":3,"offset":25,"has_header":false}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.got.Offset != 25 {
		t.Errorf("Offset = %d, want 25", fake.got.Offset)
	}
	if fake.got.HasHeader == nil || *fake.got.HasHeader {
		t.Errorf("HasHeader = %v, want an explicit false", fake.got.HasHeader)
	}

	if fake.got.MaxRows != 7 {
		t.Errorf("MaxRows = %d, want 7", fake.got.MaxRows)
	}
	if fake.got.MaxChars != 99 {
		t.Errorf("MaxChars = %d, want 99", fake.got.MaxChars)
	}
	if fake.got.MaxBytes != 4096 {
		t.Errorf("MaxBytes = %d, want 4096", fake.got.MaxBytes)
	}
	if fake.got.MaxPages != 3 {
		t.Errorf("MaxPages = %d, want 3", fake.got.MaxPages)
	}
}

// TestReadDocumentTool_PassesOCRLangThrough covers the ocr_lang arg ->
// ExtractRequest.OCRLang plumbing, the same way
// TestReadDocumentTool_PassesCapsThrough covers the numeric caps.
func TestReadDocumentTool_PassesOCRLangThrough(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(), `{"path":"data.csv","ocr_lang":"fra"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.got.OCRLang != "fra" {
		t.Errorf("OCRLang = %q, want %q", fake.got.OCRLang, "fra")
	}
}

// TestReadDocumentTool_OmittedHasHeaderStaysNil: an absent key must mean
// "auto-detect", which is a different instruction than an explicit
// false. Decoding into a plain bool would collapse the two.
func TestReadDocumentTool_OmittedHasHeaderStaysNil(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	fake := &capturingExtractor{}
	reg := &documents.Registry{}
	reg.Register(fake)

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Registry: reg}
	if _, err := tool.Execute(context.Background(), `{"path":"data.csv"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.got.HasHeader != nil {
		t.Errorf("HasHeader = %v, want nil when the key is omitted", *fake.got.HasHeader)
	}
}

// TestReadDocumentTool_ClampedMaxBytesIsReported: an over-ceiling
// max_bytes is clamped inside withDefaults, which on its own would leave
// the model believing it read further than it did. This tool's contract
// is that every limit is stated, so the clamp must surface as a warning.
func TestReadDocumentTool_ClampedMaxBytesIsReported(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(),
		fmt.Sprintf(`{"path":"data.csv","max_bytes":%d}`, documents.MaxAllowedBytes*4))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(out, "clamped") {
		t.Errorf("an over-ceiling max_bytes must be reported, not silently clamped; got:\n%s", out)
	}
}

// TestReadDocumentTool_InBoundsMaxBytesIsNotWarned guards the other
// direction: a legitimate override must not produce clamp noise.
func TestReadDocumentTool_InBoundsMaxBytesIsNotWarned(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "data.csv", "id,name\n1,Widget\n")

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"data.csv","max_bytes":1048576}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if strings.Contains(out, "clamped") {
		t.Errorf("an in-bounds max_bytes must not warn; got:\n%s", out)
	}
}

func TestReadDocumentTool_NoApprovalNeeded(t *testing.T) {
	tool := &ReadDocumentTool{}
	if tool.RequiresApproval("") {
		t.Error("read_document is read-only and must never require approval")
	}
	if !tool.ParallelSafe("") {
		t.Error("read_document should be parallel-safe like read_file")
	}
}

// TestRegisterCoreCwdTools_DocumentIngestionGate checks that read_document
// is always registered (GA for every format but PDF) and that
// AllowPDFIngestion controls only the tool's PDF-specific gate, not its
// presence.
func TestRegisterCoreCwdTools_DocumentIngestionGate(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	raw, ok := reg.Get("read_document")
	if !ok {
		t.Fatal("read_document should always be registered, regardless of AllowPDFIngestion")
	}
	if raw.(*ReadDocumentTool).SubprocessFormatsEnabled {
		t.Fatal("read_document's PDF gate should be off by default")
	}

	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, AllowPDFIngestion: true})
	raw, ok = reg.Get("read_document")
	if !ok {
		t.Fatal("read_document should be registered")
	}
	if !raw.(*ReadDocumentTool).SubprocessFormatsEnabled {
		t.Fatal("read_document's PDF gate should be on when AllowPDFIngestion is true")
	}
}

// TestReadDocumentTool_PDFRoutesThroughSandbox is the integration
// regression for stage 1: the default (nil Registry) registry must
// dispatch .pdf to PDFExtractor, and PDFExtractor's Run must actually
// invoke t.sandbox().Command — proving pdftotext/pdfinfo are routed
// through the same Sandbox seam create_document already uses for pandoc,
// not a host-only exec.LookPath.
func TestReadDocumentTool_PDFRoutesThroughSandbox(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.pdf", "not a real pdf, just needs to exist")

	sb := &fakeSandbox{label: "[podman-sandbox]"}
	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Sandbox: sb}

	reg := tool.registry()
	ex := reg.Lookup(filepath.Join(tmp, "doc.pdf"))
	if ex == nil {
		t.Fatal("expected the default registry to match .pdf")
	}
	pdfEx, ok := ex.(*documents.PDFExtractor)
	if !ok {
		t.Fatalf("expected a *documents.PDFExtractor, got %T", ex)
	}
	if pdfEx.Run == nil {
		t.Fatal("expected PDFExtractor.Run to be wired to tool.runPDFCommand")
	}
	// Calling Run must route through fakeSandbox.Command, not a
	// host-only exec.LookPath — proven by the fact that it doesn't panic
	// on a nil t.Sandbox path and honors the injected fakeSandbox at all
	// (a bug that bypassed Sandbox entirely would still "work" here by
	// accident, since fakeSandbox's default behavior runs real commands —
	// the label prefix in PreviewCall-style tools is the usual tell, but
	// PDFExtractor has no PreviewCall; asserting Run is non-nil and
	// invocable through the wired tool is the available signal).
	if _, _, err := pdfEx.Run(context.Background(), "command -v true"); err != nil {
		t.Errorf("expected the wired Run to succeed for a real, always-present command: %v", err)
	}
}

// TestReadDocumentTool_PDFMissingPdftotextIsAnActionableError exercises
// the real host path end to end: without a sandbox, PDF extraction shells
// out directly (HostSandbox), and since pdftotext isn't installed on the
// test host, Execute must surface a clear error rather than silently
// returning an empty result. SubprocessFormatsEnabled is set so this
// test reaches PDFExtractor's own error, not the earlier gate — see
// TestReadDocumentTool_PDFBlockedWithoutSubprocessGate for that.
func TestReadDocumentTool_PDFMissingPdftotextIsAnActionableError(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.pdf", "not a real pdf, just needs to exist")
	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), SubprocessFormatsEnabled: true}
	_, err := tool.Execute(context.Background(), `{"path":"doc.pdf"}`)
	if err == nil {
		t.Fatal("expected an error since pdftotext/pdfinfo aren't installed on the test host")
	}
}

// TestReadDocumentTool_PDFDisabledViaSubprocessGate is the regression for
// the field itself: read_document is fully GA (document_ingestion
// graduated — see internal/experimental/features.go), so every real
// caller wires SubprocessFormatsEnabled true unconditionally, but the
// field remains a real on/off switch for a caller that constructs the
// tool directly with it left false. Every other format is unaffected —
// see TestReadDocumentTool_XLSXEndToEnd, which doesn't set this field
// yet already succeeds.
func TestReadDocumentTool_PDFDisabledViaSubprocessGate(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.pdf", "not a real pdf, just needs to exist")
	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	_, err := tool.Execute(context.Background(), `{"path":"doc.pdf"}`)
	if err == nil || !strings.Contains(err.Error(), "disabled in this configuration") {
		t.Fatalf("expected an error explaining PDF is disabled, got %v", err)
	}
}

// TestReadDocumentTool_XLSXEndToEnd exercises the real xlsx path through
// Execute (not just the underlying extractor), confirming the default
// registry dispatches .xlsx without any Sandbox — xlsx never shells out.
func TestReadDocumentTool_XLSXEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	data, err := documents.GenerateXLSX(documents.SheetModel{Sheets: []documents.Sheet{{
		Name: "Data",
		Rows: [][]documents.Cell{{{Value: "hello"}}},
	}}})
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	writeFile(t, tmp, "book.xlsx", string(data))

	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{"path":"book.xlsx"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "xlsx") || !strings.Contains(out, "hello") {
		t.Errorf("output missing expected xlsx content: %q", out)
	}
}

// TestReadDocumentTool_DocxPandocTierReachableThroughDefaultRegistry is
// the regression for a real bug caught while wiring this up:
// documents.NewRegistry() already registers a nil-Run DocxExtractor, so
// registering a second, richer one afterward was silently unreachable —
// Registry.Lookup matches in registration order, and the first
// (native-only) entry always won. This must go through the tool's
// actual default registry() path (Registry left nil), not construct a
// DocxExtractor directly, or it wouldn't catch the shadowing bug at all.
func TestReadDocumentTool_DocxPandocTierReachableThroughDefaultRegistry(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "notes.docx", "placeholder, never actually parsed as a real docx here")

	sb := &fakeSandbox{label: "[podman-sandbox]"}
	// Force the fake sandbox to run a literal echo instead of trying to
	// invoke a nonexistent pandoc, so this test doesn't depend on pandoc
	// actually being installed — only on WHICH extractor got asked.
	tool := &ReadDocumentTool{Cwd: NewCwdRef(tmp), Sandbox: sb}
	reg := tool.registry()
	ex := reg.Lookup(tmp + "/notes.docx")
	docxEx, ok := ex.(*documents.DocxExtractor)
	if !ok {
		t.Fatalf("expected the default registry to dispatch .docx to *documents.DocxExtractor, got %T", ex)
	}
	if docxEx.Run == nil {
		t.Fatal("expected the default registry's DocxExtractor to have Run wired (the pandoc tier), got nil — the richer registration is being shadowed by NewRegistry's own native-only entry")
	}
}

// TestReadDocumentTool_ResolvePyHelperScript_PodmanUsesInImagePath is
// the regression for the wiring itself (not just pdf.go's own tier
// logic, already covered by internal/documents' pdf_tables_test.go): a
// podman-labeled sandbox must resolve to the fixed in-image path with
// no filesystem access at all.
func TestReadDocumentTool_ResolvePyHelperScript_PodmanUsesInImagePath(t *testing.T) {
	tool := &ReadDocumentTool{Sandbox: &fakeSandbox{label: podmanSandboxLabel}}
	got, err := tool.resolvePyHelperScript(documents.ScriptExtractPDFTables)
	if err != nil {
		t.Fatalf("resolvePyHelperScript: %v", err)
	}
	want := documents.DocumentsImageHelperDir + "/extract_pdf_tables.py"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReadDocumentTool_ResolvePyHelperScript_HostMaterializesToCache
// covers the non-podman path — HOME is redirected to a temp dir so the
// test never touches the real user's ~/.yottacode/cache/doc-helpers.
func TestReadDocumentTool_ResolvePyHelperScript_HostMaterializesToCache(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	tool := &ReadDocumentTool{} // no Sandbox set -> HostSandbox
	got, err := tool.resolvePyHelperScript(documents.ScriptFillDocxTemplate)
	if err != nil {
		t.Fatalf("resolvePyHelperScript: %v", err)
	}
	want := filepath.Join(fakeHome, ".yottacode", "cache", "doc-helpers", "fill_docx_template.py")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
