package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoSyntaxSourceRangesReturnsSmallestToLargest(t *testing.T) {
	path := writeGoSyntaxFixture(t, `package main

func run() {
	if true {
		println("target")
	}
}
`)
	ranges, err := goSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 4, Character: 10})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	if len(ranges) < 4 {
		t.Fatalf("expected block/if/function/file ranges, got %#v", ranges)
	}
	wantPrefix := []string{"block", "if", "block", "function"}
	for i, want := range wantPrefix {
		if ranges[i].Kind != want {
			t.Fatalf("ranges[%d].Kind = %q, want %q; all=%#v", i, ranges[i].Kind, want, ranges)
		}
	}
	if ranges[3].Name != "run" {
		t.Fatalf("function range name = %q, want run", ranges[3].Name)
	}
}

func TestGoSyntaxSourceRangesNamesMethodsAndTypes(t *testing.T) {
	path := writeGoSyntaxFixture(t, `package main

type Widget struct {
	Name string
}

func (w *Widget) Method() {
	println(w.Name)
}
`)
	ranges, err := goSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 7, Character: 9})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var method SyntaxRange
	for _, r := range ranges {
		if r.Kind == "method" {
			method = r
			break
		}
	}
	if method.Name != "Method" || method.Detail != "Widget" {
		t.Fatalf("method range = %#v", method)
	}
}

func TestGoSyntaxSourceRangesRejectsInvalidPosition(t *testing.T) {
	path := writeGoSyntaxFixture(t, "package main\nfunc run() {}\n")
	_, err := goSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 99, Character: 0})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected outside-document error, got %v", err)
	}
}

func TestSyntaxFileRangesReportsUnsupportedSource(t *testing.T) {
	lang := Language{ID: "typescript"}
	ranges, ok, err := SyntaxFileRanges(context.Background(), lang, "app.ts", Position{})
	if err != nil {
		t.Fatalf("SyntaxFileRanges: %v", err)
	}
	if ok || ranges != nil {
		t.Fatalf("typescript should not have parser-backed ranges yet: ok=%v ranges=%#v", ok, ranges)
	}
}

func writeGoSyntaxFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.go")
	writeFallbackTestFile(t, path, body)
	return path
}
