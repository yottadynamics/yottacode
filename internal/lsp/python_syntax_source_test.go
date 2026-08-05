package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonSyntaxSourceRangesOrdering(t *testing.T) {
	path := writePythonFixture(t, "main.py", `class Widget:
    def method(self):
        if True:
            print("target")
`)
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 3, Character: 16})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	if len(ranges) < 4 {
		t.Fatalf("expected if/method/class/file ranges, got %#v", ranges)
	}
	wantPrefix := []string{"if", "method", "class", "file"}
	for i, want := range wantPrefix {
		if ranges[i].Kind != want {
			t.Fatalf("ranges[%d].Kind = %q, want %q; all=%#v", i, ranges[i].Kind, want, ranges)
		}
	}
	if ranges[1].Name != "method" || ranges[1].Detail != "Widget" {
		t.Fatalf("method range = %#v", ranges[1])
	}
}

func TestPythonSyntaxSourceDedentClosesBlock(t *testing.T) {
	path := writePythonFixture(t, "dedent.py", `def run():
    if True:
        pass
    return 1
`)
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 3, Character: 6})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	for _, r := range ranges {
		if r.Kind == "if" {
			t.Fatalf("dedented `if` block should not contain line 3, got %#v", r)
		}
	}
	var haveFunc bool
	for _, r := range ranges {
		if r.Kind == "function" && r.Name == "run" {
			haveFunc = true
			if r.Range.End.Line != 3 {
				t.Fatalf("function range should extend through the dedented return, got %#v", r)
			}
		}
	}
	if !haveFunc {
		t.Fatalf("expected function range, got %#v", ranges)
	}
}

func TestPythonSyntaxSourceIgnoresColonInDocstring(t *testing.T) {
	path := writePythonFixture(t, "doc.py", `def run():
    """
    not a block: still just a docstring
    """
    return 1
`)
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 4, Character: 4})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	for _, r := range ranges {
		if r.Name == "not a block" {
			t.Fatalf("docstring content must not be parsed as a block header, got %#v", ranges)
		}
	}
	var haveFunc bool
	for _, r := range ranges {
		if r.Kind == "function" && r.Name == "run" {
			haveFunc = true
		}
	}
	if !haveFunc {
		t.Fatalf("expected function range spanning the docstring, got %#v", ranges)
	}
}

func TestPythonSyntaxSourceMultilineHeaderWraps(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"trailing comma", "def run(\n    a,\n    b,\n):\n    return a + b\n"},
		{"no trailing comma", "def run(\n    a,\n    b\n):\n    return a + b\n"},
		{"single continuation line", "def run(a,\n         b):\n    return a + b\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writePythonFixture(t, "wrap.py", c.body)
			lastLine := strings.Count(c.body, "\n") - 1
			ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: lastLine, Character: 4})
			if err != nil {
				t.Fatalf("Ranges: %v", err)
			}
			var haveFunc bool
			for _, r := range ranges {
				if r.Kind == "function" && r.Name == "run" {
					haveFunc = true
				}
			}
			if !haveFunc {
				t.Fatalf("expected function range covering the body despite a wrapped signature, got %#v", ranges)
			}
		})
	}
}

func TestPythonSyntaxSourceMultilineHeaderNestedBrackets(t *testing.T) {
	path := writePythonFixture(t, "nested.py", "def run(\n    a,\n    b=[1, 2, {\n        3: 4,\n    }],\n):\n    return a\n")
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 6, Character: 4})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var haveFunc bool
	for _, r := range ranges {
		if r.Kind == "function" && r.Name == "run" {
			haveFunc = true
		}
	}
	if !haveFunc {
		t.Fatalf("expected function range despite nested brackets in a default value, got %#v", ranges)
	}
}

func TestPythonSyntaxSourceMultilineNonBlockDoesNotOpenFrame(t *testing.T) {
	path := writePythonFixture(t, "dict.py", "x = {\n    1: 2,\n    3: 4,\n}\ny = 1\n")
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 4, Character: 0})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	if len(ranges) != 1 || ranges[0].Kind != "file" {
		t.Fatalf("a multi-line dict literal is not a block; expected only the file range, got %#v", ranges)
	}
}

func TestPythonSyntaxSourceResyncAfterUnclosedBracket(t *testing.T) {
	path := writePythonFixture(t, "resync.py", "def broken(\n    a,\ndef other():\n    return 1\n")
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 3, Character: 4})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var haveOther bool
	for _, r := range ranges {
		if r.Kind == "function" && r.Name == "other" {
			haveOther = true
		}
	}
	if !haveOther {
		t.Fatalf("an unclosed bracket earlier in the file must not black out later, well-formed functions; got %#v", ranges)
	}
}

func TestPythonSyntaxSourceResyncPreservesEnclosingClass(t *testing.T) {
	path := writePythonFixture(t, "resync_class.py", "class Widget:\n    def broken(\n        a,\n    def method(self):\n        return 1\n")
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 4, Character: 8})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var method, class SyntaxRange
	for _, r := range ranges {
		switch r.Kind {
		case "method":
			method = r
		case "class":
			class = r
		}
	}
	// Resync must compute the recovered line's indent from the true start of
	// its physical line, not from the resync keyword's own token offset —
	// otherwise the recovered frame reads as indent 0, which incorrectly
	// closes (and truncates the range of) the still-open enclosing class.
	if method.Name != "method" || method.Detail != "Widget" {
		t.Fatalf("resync must still attribute the recovered method to its enclosing class, got %#v", method)
	}
	if class.Name != "Widget" || class.Range.End.Line != 4 {
		t.Fatalf("resync must not truncate the enclosing class's range, got %#v", class)
	}
}

func TestPythonSyntaxSourceComprehensionKeywordsDoNotTriggerResync(t *testing.T) {
	path := writePythonFixture(t, "comprehension.py", "def run(\n    items=[x for x in range(10) if x > 2],\n):\n    return items\n")
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 3, Character: 4})
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	var haveFunc bool
	for _, r := range ranges {
		if r.Kind == "function" && r.Name == "run" {
			haveFunc = true
		}
	}
	if !haveFunc {
		t.Fatalf("a comprehension's for/if inside brackets must not falsely trigger resync, got %#v", ranges)
	}
}

func TestPythonSyntaxSourceAsyncDef(t *testing.T) {
	path := writePythonFixture(t, "async.py", `class Service:
    async def fetch(self):
        return 1
`)
	ranges, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 2, Character: 10})
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
	if method.Name != "fetch" || method.Detail != "Service" {
		t.Fatalf("async method range = %#v; all=%#v", method, ranges)
	}
}

func TestPythonSyntaxSourceRangesRejectsInvalidPosition(t *testing.T) {
	path := writePythonFixture(t, "invalid.py", "def run():\n    pass\n")
	_, err := pythonSyntaxSource{}.Ranges(context.Background(), path, Position{Line: 99, Character: 0})
	if err == nil || !strings.Contains(err.Error(), "outside document") {
		t.Fatalf("expected outside-document error, got %v", err)
	}
}

func TestPythonSyntaxSourceSymbols(t *testing.T) {
	path := writePythonFixture(t, "symbols.py", `class Widget:
    def method(self):
        pass


def run():
    pass
`)
	symbols, err := pythonSyntaxSource{}.Symbols(context.Background(), path)
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

func TestPythonSyntaxSourceRangesHonorsCanceledContext(t *testing.T) {
	path := writePythonFixture(t, "cancel.py", "def run():\n    return 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := pythonSyntaxSource{}
	if _, err := source.Ranges(ctx, path, Position{Line: 1, Character: 4}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ranges: got %v, want context.Canceled", err)
	}
	if _, err := source.Symbols(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Symbols: got %v, want context.Canceled", err)
	}
}

func writePythonFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFallbackTestFile(t, path, body)
	return path
}
