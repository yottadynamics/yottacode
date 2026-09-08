package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestBraceSyntaxSourceTypeScriptRangesOrdering(t *testing.T) {
	path := writeBraceFixture(t, "main.ts", `class Widget {
  method() {
    if (true) {
      console.log("target");
    }
  }
}
`)
	ranges, err := braceSyntaxSource{spec: tsBraceSpec}.Ranges(context.Background(), path, Position{Line: 3, Character: 20})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	if len(ranges) < 4 {
		t.Fatalf("expected if/method/class/file ranges, got %#v", ranges)
	}
	wantPrefix := []SyntaxKind{"if", "method", "class", "file"}
	for i, want := range wantPrefix {
		if ranges[i].Kind != want {
			t.Fatalf("ranges[%d].Kind = %q, want %q; all=%#v", i, ranges[i].Kind, want, ranges)
		}
	}
	if ranges[1].Name != "method" || ranges[1].Detail != "Widget" {
		t.Fatalf("method range = %#v", ranges[1])
	}
}

func TestBraceSyntaxSourceTypeScriptArrowFunction(t *testing.T) {
	path := writeBraceFixture(t, "arrow.ts", `const handler = () => {
  return 1;
};
`)
	ranges, err := braceSyntaxSource{spec: tsBraceSpec}.Ranges(context.Background(), path, Position{Line: 1, Character: 3})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var found bool
	for _, r := range ranges {
		if r.Kind == SyntaxKindFunction && r.Name == "handler" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected arrow function named handler, got %#v", ranges)
	}
}

func TestBraceSyntaxSourceTypeScriptCallUsesFullDottedCallee(t *testing.T) {
	path := writeBraceFixture(t, "call.ts", "function run() {\n  console.log(\"target\");\n}\n")
	ranges, err := braceSyntaxSource{spec: tsBraceSpec}.Ranges(context.Background(), path, Position{Line: 1, Character: 15})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	for _, r := range ranges {
		if r.Kind == SyntaxKindCall {
			if r.Name != "console.log" || r.Range.Start.Line != 1 || r.Range.Start.Character != 2 {
				t.Fatalf("call range = %#v, want full dotted callee", r)
			}
			return
		}
	}
	t.Fatalf("expected call range, got %#v", ranges)
}

func TestBraceSyntaxSourceDoesNotTreatDeclarationParametersAsCall(t *testing.T) {
	cases := []struct {
		name string
		spec braceLanguageSpec
		file string
		body string
		pos  Position
	}{
		{name: "typescript", spec: tsBraceSpec, file: "decl.ts", body: "function run(value: string) {\n  return value;\n}\n", pos: Position{Line: 0, Character: 15}},
		{name: "rust", spec: rustBraceSpec, file: "decl.rs", body: "fn run(value: String) {\n    println!(\"{}\", value);\n}\n", pos: Position{Line: 0, Character: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeBraceFixture(t, tc.file, tc.body)
			ranges, err := braceSyntaxSource{spec: tc.spec}.Ranges(context.Background(), path, tc.pos)
			if err != nil {
				t.Fatalf("Ranges: %v", err)
			}
			for _, r := range ranges {
				if r.Kind == SyntaxKindCall {
					t.Fatalf("declaration parameters must not be emitted as a call: %#v", ranges)
				}
			}
		})
	}
}

func TestBraceSyntaxSourceRustImplRanges(t *testing.T) {
	path := writeBraceFixture(t, "widget.rs", `struct Widget {
    name: String,
}

impl Widget {
    fn greet(&self) {
        println!("hi");
    }
}
`)
	ranges, err := braceSyntaxSource{spec: rustBraceSpec}.Ranges(context.Background(), path, Position{Line: 5, Character: 10})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var method SyntaxRange
	for _, r := range ranges {
		if r.Kind == SyntaxKindMethod {
			method = r
			break
		}
	}
	if method.Name != "greet" || method.Detail != "Widget" {
		t.Fatalf("method range = %#v; all=%#v", method, ranges)
	}
}

func TestBraceSyntaxSourceRustImplTraitForName(t *testing.T) {
	path := writeBraceFixture(t, "trait_impl.rs", `impl<T: Clone> Greeter for Widget<T> {
    fn greet(&self) {}
}
`)
	ranges, err := braceSyntaxSource{spec: rustBraceSpec}.Ranges(context.Background(), path, Position{Line: 1, Character: 5})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var impl SyntaxRange
	for _, r := range ranges {
		if r.Kind == SyntaxKind("impl") {
			impl = r
			break
		}
	}
	if impl.Name != "Greeter for Widget" {
		t.Fatalf("impl range name = %q, want %q; all=%#v", impl.Name, "Greeter for Widget", ranges)
	}
}

func TestBraceSyntaxSourceIgnoresBracesInStringsAndComments(t *testing.T) {
	path := writeBraceFixture(t, "strings.rs", `fn run() {
    let s = "{ not a block }";
    // a comment with a { brace too
    let t = "another } one";
}
`)
	ranges, err := braceSyntaxSource{spec: rustBraceSpec}.Ranges(context.Background(), path, Position{Line: 1, Character: 4})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var fnCount int
	for _, r := range ranges {
		if r.Kind == SyntaxKind("fn") {
			fnCount++
			if r.Range.Start.Line != 0 || r.Range.End.Line != 4 {
				t.Fatalf("function range should span the whole function, got %#v", r)
			}
		}
	}
	if fnCount != 1 {
		t.Fatalf("expected exactly one function range, got %d: %#v", fnCount, ranges)
	}
}

func TestBraceSyntaxSourceCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlf.rs")
	writeFallbackTestFile(t, path, "fn run() {\r\n    let x = 1;\r\n}\r\n")
	ranges, err := braceSyntaxSource{spec: rustBraceSpec}.Ranges(context.Background(), path, Position{Line: 1, Character: 8})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var found bool
	for _, r := range ranges {
		if r.Kind == SyntaxKind("fn") && r.Name == "run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected function run range with CRLF fixture, got %#v", ranges)
	}
}

func TestBraceSyntaxSourceRangesRejectsInvalidPosition(t *testing.T) {
	path := writeBraceFixture(t, "invalid.ts", "function run() {}\n")
	_, err := braceSyntaxSource{spec: tsBraceSpec}.Ranges(context.Background(), path, Position{Line: 99, Character: 0})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected outside-document error, got %v", err)
	}
}

func TestBraceSyntaxSourceSymbols(t *testing.T) {
	path := writeBraceFixture(t, "symbols.ts", `class Widget {
  method() {}
}
function run() {}
`)
	symbols, err := braceSyntaxSource{spec: tsBraceSpec}.Symbols(context.Background(), path)
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	var haveClass, haveMethod, haveFunc bool
	for _, s := range symbols {
		switch {
		case s.Kind == "class" && s.Name == "Widget":
			haveClass = true
		case s.Kind == "method" && s.Name == "method" && s.Container == "Widget":
			haveMethod = true
		case s.Kind == "function" && s.Name == "run":
			haveFunc = true
		}
	}
	if !haveClass || !haveMethod || !haveFunc {
		t.Fatalf("missing expected symbols: class=%v method=%v func=%v; all=%#v", haveClass, haveMethod, haveFunc, symbols)
	}
}

func TestBraceSyntaxSourceRangesHonorsCanceledContext(t *testing.T) {
	path := writeBraceFixture(t, "cancel.ts", "class Widget {\n  method() {\n    return 1;\n  }\n}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := braceSyntaxSource{spec: tsBraceSpec}
	if _, err := source.Ranges(ctx, path, Position{Line: 2, Character: 4}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ranges: got %v, want context.Canceled", err)
	}
	if _, err := source.Symbols(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Symbols: got %v, want context.Canceled", err)
	}
}

func writeBraceFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFallbackTestFile(t, path, body)
	return path
}
