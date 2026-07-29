package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFallbackFileSymbolsUsesGoParser(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "main.go")
	writeFallbackTestFile(t, path, `package main

type Widget struct{}
const (
	Answer = 42
	other = "x"
)
var global int
func Top() {}
func (w *Widget) Method() {}
`)
	items, err := FallbackFileSymbols(path)
	if err != nil {
		t.Fatalf("FallbackFileSymbols: %v", err)
	}
	got := map[string]Symbol{}
	for _, item := range items {
		got[item.Name] = item
	}
	for _, name := range []string{"Widget", "Answer", "other", "global", "Top", "Method"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %s in %#v", name, items)
		}
	}
	if got["Method"].Kind != "method" || got["Method"].Container != "Widget" {
		t.Fatalf("method symbol = %#v", got["Method"])
	}
	if got["Answer"].Kind != "constant" || got["global"].Kind != "variable" {
		t.Fatalf("unexpected value symbol kinds: Answer=%#v global=%#v", got["Answer"], got["global"])
	}
	if got["Top"].Container != "parser" {
		t.Fatalf("Go parser-backed symbol should be marked parser, got %#v", got["Top"])
	}
}

func TestFallbackSymbolsFiltersParserResults(t *testing.T) {
	tmp := t.TempDir()
	writeFallbackTestFile(t, filepath.Join(tmp, "main.go"), "package main\nfunc Alpha() {}\nfunc Beta() {}\n")
	items, err := FallbackSymbols(context.Background(), tmp, "alp", 100)
	if err != nil {
		t.Fatalf("FallbackSymbols: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Alpha" {
		t.Fatalf("filtered fallback items = %#v", items)
	}
}

func TestFallbackSymbolsMaxFilesCountsSupportedFilesOnly(t *testing.T) {
	tmp := t.TempDir()
	for _, rel := range []string{"aaa/one.md", "aaa/two.txt", "aaa/three.json"} {
		writeFallbackTestFile(t, filepath.Join(tmp, rel), "not source")
	}
	writeFallbackTestFile(t, filepath.Join(tmp, "zzz/main.go"), "package main\nfunc Target() {}\n")
	items, err := FallbackSymbols(context.Background(), tmp, "target", 1)
	if err != nil {
		t.Fatalf("FallbackSymbols: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Target" {
		t.Fatalf("maxFiles should count supported source files only, got %#v", items)
	}
}

func TestFallbackFileSymbolsKeepsRegexForOtherLanguages(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "app.ts")
	writeFallbackTestFile(t, path, "export function run() {}\nexport class Box {}\n")
	items, err := FallbackFileSymbols(path)
	if err != nil {
		t.Fatalf("FallbackFileSymbols: %v", err)
	}
	got := map[string]Symbol{}
	for _, item := range items {
		got[item.Name] = item
	}
	if got["run"].Container != "fallback" || got["Box"].Kind != "class" {
		t.Fatalf("TypeScript regex fallback regressed: %#v", items)
	}
}

func TestSyntaxModeReportsFallbackCapability(t *testing.T) {
	cases := map[string]string{"go": "parser", "typescript": "regex", "python": "regex", "rust": "regex", "ruby": "none"}
	for id, want := range cases {
		if got := SyntaxMode(id); got != want {
			t.Fatalf("SyntaxMode(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestPositionForOffsetUsesUTF16Columns(t *testing.T) {
	text := "emoji 😀 old\naccent é old\n"
	pos, err := PositionForOffset(text, len("emoji 😀 "))
	if err != nil {
		t.Fatalf("PositionForOffset: %v", err)
	}
	if pos.Line != 0 || pos.Character != 9 {
		t.Fatalf("position = %#v, want line 0 char 9", pos)
	}
	if _, err := PositionForOffset(text, len("emoji ")+1); err == nil {
		t.Fatal("expected split-rune offset to fail")
	}
}

func writeFallbackTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
