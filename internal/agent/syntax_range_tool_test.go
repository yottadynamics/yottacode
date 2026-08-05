package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntaxRangeToolExecuteGoRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	writeFile(t, dir, "main.go", `package main

func run() {
	if true {
		println("target")
	}
}
`)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":4,"character":10}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"block [parser]", "function run", "anchor_read=", `"anchors":true`, path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSyntaxRangeToolExecuteTypeScriptRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "widget.ts")
	writeFile(t, dir, "widget.ts", `class Widget {
  method() {
    if (true) {
      console.log("target");
    }
  }
}
`)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"widget.ts","line":3,"character":20}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"method [Widget]", "class Widget", "anchor_read=", `"anchors":true`, path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSyntaxRangeToolExecutePythonRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "widget.py")
	writeFile(t, dir, "widget.py", `class Widget:
    def method(self):
        if True:
            print("target")
`)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"widget.py","line":3,"character":16}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"method [Widget]", "class Widget", "anchor_read=", `"anchors":true`, path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSyntaxRangeToolExecuteRustRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "widget.rs")
	writeFile(t, dir, "widget.rs", `struct Widget {
    name: String,
}

impl Widget {
    fn greet(&self) {
        println!("hi");
    }
}
`)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"widget.rs","line":5,"character":10}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"fn greet [Widget]", "impl Widget", "anchor_read=", `"anchors":true`, path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSyntaxRangeToolUnsupportedLanguage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "# hi\n")
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"note.md","line":0,"character":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "unavailable: syntax ranges are not available") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestSyntaxRangeToolMaxResults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\nfunc run() {\n\tprintln(1)\n}\n")
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":2,"character":2,"max_results":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "…[truncated at 1 results]") {
		t.Fatalf("expected truncation, got:\n%s", out)
	}
}

func TestRegisterCoreCwdTools_SyntaxRangeGate(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}})
	if reg.Names()["syntax_range"] {
		t.Fatal("syntax_range should be absent when syntax_ranges is disabled")
	}
	reg = NewRegistry()
	RegisterCoreCwdTools(reg, cwd, CoreToolDeps{WriteOpts: WritePathOptions{Cwd: cwd}, EnableSyntaxRanges: true})
	if !reg.Names()["syntax_range"] {
		t.Fatal("syntax_range should be registered when syntax_ranges is enabled")
	}
}
