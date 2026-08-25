package documents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePyHelperScript_Podman(t *testing.T) {
	got, err := ResolvePyHelperScript(ScriptExtractPDFTables, true, t.TempDir())
	if err != nil {
		t.Fatalf("ResolvePyHelperScript: %v", err)
	}
	want := DocumentsImageHelperDir + "/extract_pdf_tables.py"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolvePyHelperScript_HostMaterializesEmbeddedContent(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolvePyHelperScript(ScriptFillDocxTemplate, false, dir)
	if err != nil {
		t.Fatalf("ResolvePyHelperScript: %v", err)
	}
	wantPath := filepath.Join(dir, "fill_docx_template.py")
	if got != wantPath {
		t.Errorf("path = %q, want %q", got, wantPath)
	}
	onDisk, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read materialized script: %v", err)
	}
	embedded, err := pyHelperScripts.ReadFile("pyhelpers/fill_docx_template.py")
	if err != nil {
		t.Fatalf("read embedded script: %v", err)
	}
	if string(onDisk) != string(embedded) {
		t.Error("materialized script content does not match the embedded source")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat materialized script: %v", err)
	}
	// The helpers are invoked as `python3 helper.py`, so executable bits are not
	// needed. Keep host-cache permissions narrow: user/group readable, not
	// world-writable/executable.
	if gotMode := info.Mode().Perm(); gotMode != 0o644 {
		t.Errorf("materialized script mode = %o, want 0644", gotMode)
	}
}

// TestResolvePyHelperScript_HostRepairsStaleCache is the regression for
// the freshness-without-a-version-file design: a cached script whose
// content has drifted from the embedded source (e.g. an old binary's
// leftover copy) must be overwritten, not trusted just because a file
// already exists at that path.
func TestResolvePyHelperScript_HostRepairsStaleCache(t *testing.T) {
	dir := t.TempDir()
	stalePath := filepath.Join(dir, "extract_pdf_tables.py")
	if err := os.WriteFile(stalePath, []byte("# stale content from an old build\n"), 0o644); err != nil {
		t.Fatalf("seed stale cache file: %v", err)
	}

	got, err := ResolvePyHelperScript(ScriptExtractPDFTables, false, dir)
	if err != nil {
		t.Fatalf("ResolvePyHelperScript: %v", err)
	}
	onDisk, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read repaired script: %v", err)
	}
	embedded, err := pyHelperScripts.ReadFile("pyhelpers/extract_pdf_tables.py")
	if err != nil {
		t.Fatalf("read embedded script: %v", err)
	}
	if string(onDisk) != string(embedded) {
		t.Error("stale cached script was not repaired to match the embedded source")
	}
}

func TestResolvePyHelperScript_HostSecondCallIsNoOp(t *testing.T) {
	dir := t.TempDir()
	first, err := ResolvePyHelperScript(ScriptExtractPDFTables, false, dir)
	if err != nil {
		t.Fatalf("first ResolvePyHelperScript: %v", err)
	}
	info1, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	second, err := ResolvePyHelperScript(ScriptExtractPDFTables, false, dir)
	if err != nil {
		t.Fatalf("second ResolvePyHelperScript: %v", err)
	}
	if first != second {
		t.Fatalf("path changed between calls: %q vs %q", first, second)
	}
	info2, err := os.Stat(second)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// A content-identical cache hit should not have rewritten the file
	// (the ModTime comparison here is a proxy for "no unnecessary
	// temp-file-then-rename churn on the common path").
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("second call rewrote an already-up-to-date cached script")
	}
}
