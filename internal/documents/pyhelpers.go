package documents

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// pyHelperScripts embeds the Python driver scripts that back the
// optional PDF-table-extraction and docx-template-fill tiers — see
// roadmap/document-generation.md's "Python-in-container implementation
// plan". This is the single source of truth for their content;
// infra/documents.Containerfile's COPY instruction bakes the same
// files (read from disk at image-build time, not from this embed) into
// the documents image at DocumentsImageHelperDir, so a podman-sandboxed
// call never touches the embed/cache path in ResolvePyHelperScript at
// all — same source, two delivery mechanisms.
//
//go:embed pyhelpers/*.py
var pyHelperScripts embed.FS

// PyHelperScript names one of the embedded driver scripts. The string
// value is also its filename, both under pyhelpers/ in this embed and
// under DocumentsImageHelperDir in the documents image.
type PyHelperScript string

const (
	// ScriptExtractPDFTables wraps pdfplumber: prints
	// {"pages": [{"page": N, "tables": [{"rows": [...]}]}]} for a PDF.
	ScriptExtractPDFTables PyHelperScript = "extract_pdf_tables.py"

	// ScriptFillDocxTemplate wraps python-docx: replaces {{name}}
	// tokens in an existing docx and saves the result.
	ScriptFillDocxTemplate PyHelperScript = "fill_docx_template.py"
)

// DocumentsImageHelperDir is the fixed path infra/documents.Containerfile
// COPYs the driver scripts to inside the documents image. A
// podman-sandboxed call invokes a script directly at this path — see
// ResolvePyHelperScript.
const DocumentsImageHelperDir = "/opt/yottacode/doc-helpers"

// ResolvePyHelperScript returns the filesystem path script should be
// invoked at, given whether the active command sandbox is the
// documents-image-backed podman sandbox (scripts already baked in at
// DocumentsImageHelperDir by the Containerfile) or anything else —
// HostSandbox, or any other future backend — in which case the
// embedded script is materialized under cacheDir once.
//
// isPodmanSandbox is a caller-supplied bool rather than this package
// inspecting a Sandbox itself: internal/documents cannot import
// internal/agent (see this package's own dependency-direction note in
// pdf.go's CommandRunner doc comment), so the caller — which owns the
// Sandbox interface — makes that call and passes the answer in, the
// same pattern CommandRunner itself already uses for the
// pandoc/pdftotext subprocess seam.
//
// The materialized copy is refreshed whenever its on-disk content
// doesn't match the embedded content byte-for-byte (missing, corrupted,
// or left over from an older yottacode binary with different script
// source) — no separate version file to keep in sync; the comparison
// itself is the freshness check.
func ResolvePyHelperScript(script PyHelperScript, isPodmanSandbox bool, cacheDir string) (string, error) {
	if isPodmanSandbox {
		return filepath.Join(DocumentsImageHelperDir, string(script)), nil
	}

	want, err := pyHelperScripts.ReadFile("pyhelpers/" + string(script))
	if err != nil {
		return "", fmt.Errorf("documents: embedded script %q not found: %w", script, err)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("documents: create script cache dir %s: %w", cacheDir, err)
	}
	cached := filepath.Join(cacheDir, string(script))

	if got, err := os.ReadFile(cached); err == nil && bytes.Equal(got, want) {
		return cached, nil
	}

	// Temp-file-then-rename so a concurrent reader (another session
	// sharing the same cache dir) never observes a partially-written
	// script.
	tmp, err := os.CreateTemp(cacheDir, "."+string(script)+".*")
	if err != nil {
		return "", fmt.Errorf("documents: create temp script file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(want); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("documents: write temp script file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("documents: close temp script file: %w", err)
	}
	// The helpers are invoked via `python3 helper.py`, not as executables. Make
	// the cache copy readable for normal host users while avoiding executable or
	// world-writable bits from the temp-file default.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("documents: chmod temp script file: %w", err)
	}
	if err := os.Rename(tmpPath, cached); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("documents: place script at %s: %w", cached, err)
	}
	return cached, nil
}
