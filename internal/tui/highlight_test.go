package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHighlightCode_EmptyReturnsEmpty(t *testing.T) {
	if got := HighlightCode("", "go"); got != "" {
		t.Errorf("empty source should return empty, got %q", got)
	}
}

func TestHighlightCode_GoSourceProducesANSI(t *testing.T) {
	src := "package main\n\nfunc main() {}\n"
	got := HighlightCode(src, "go")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("highlighted Go should contain ANSI escapes: %q", got)
	}
	// The original tokens (package, main, func) should still be
	// present underneath the escapes.
	for _, want := range []string{"package", "main", "func"} {
		if !strings.Contains(got, want) {
			t.Errorf("highlighted output should preserve token %q: %q", want, got)
		}
	}
}

func TestHighlightCode_UnknownLanguageFallsBack(t *testing.T) {
	src := "anything goes here"
	got := HighlightCode(src, "klingon-script-9000")
	// Doesn't crash, returns SOMETHING containing the original text.
	if !strings.Contains(stripANSI(got), "anything goes here") {
		t.Errorf("fallback should preserve content: %q", got)
	}
}

func TestHighlightCode_HintEmptyTriggersAnalysis(t *testing.T) {
	// An empty hint with content that strongly suggests a language
	// should still highlight via chroma's content analysis.
	src := "package foo\n\nfunc bar() int { return 1 }\n"
	got := HighlightCode(src, "")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("content-analysed Go should still highlight: %q", got)
	}
}

func TestHighlightFromPath_DerivesFromExtension(t *testing.T) {
	cases := []struct {
		path string
		src  string
	}{
		{"main.go", "package main"},
		{"app.py", "def hello(): pass"},
		{"index.ts", "const x: number = 1"},
		{"Cargo.toml", "[package]"},
	}
	for _, c := range cases {
		got := HighlightFromPath(c.src, c.path)
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("path %q should highlight content: %q", c.path, got)
		}
	}
}

func TestHighlightFromPath_UnknownExtensionStillPasses(t *testing.T) {
	src := "raw content with no extension"
	got := HighlightFromPath(src, "/tmp/no-extension-here")
	if !strings.Contains(stripANSI(got), "raw content") {
		t.Errorf("unknown-extension fallback should preserve content: %q", got)
	}
}

func TestLanguageFromPath_KnownExtensions(t *testing.T) {
	cases := map[string]string{
		"main.go":     "go",
		"app.py":      "python",
		"build.rs":    "rust",
		"cli.ts":      "typescript",
		"comp.tsx":    "tsx",
		"Makefile":    "make",
		"Dockerfile":  "docker",
		"app.yaml":    "yaml",
		"data.json":   "json",
		"page.html":   "html",
		"":            "",
	}
	for path, want := range cases {
		if got := languageFromPath(path); got != want {
			t.Errorf("languageFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestRenderWriteFileApproval — write_file approval modal rendering.

func TestRenderWriteFileApproval_HappyPath(t *testing.T) {
	args := `{"path":"main.go","content":"package main\n\nfunc main() {}\n"}`
	got, ok := renderWriteFileApproval(args)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, "main.go") {
		t.Errorf("path missing from preview: %q", plain)
	}
	if !strings.Contains(plain, "package main") {
		t.Errorf("content missing: %q", plain)
	}
	if !strings.Contains(plain, "bytes") {
		t.Errorf("byte count should be shown: %q", plain)
	}
}

func TestRenderWriteFileApproval_TruncatesLongContent(t *testing.T) {
	// 50 lines well over the 30-line cap.
	long := strings.Repeat("// filler\n", 50)
	args, _ := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{Path: "x.go", Content: long})
	got, ok := renderWriteFileApproval(string(args))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(got, "more lines") {
		t.Errorf("truncation notice missing: %q", got)
	}
}

func TestRenderWriteFileApproval_RejectsBadJSON(t *testing.T) {
	if _, ok := renderWriteFileApproval(`{not json`); ok {
		t.Errorf("expected ok=false for malformed JSON")
	}
}

func TestRenderWriteFileApproval_RequiresPath(t *testing.T) {
	if _, ok := renderWriteFileApproval(`{"content":"x"}`); ok {
		t.Errorf("expected ok=false when path is missing")
	}
}
