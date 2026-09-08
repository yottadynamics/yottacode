package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	for _, want := range []string{"method [Widget]", "type Widget", "anchor_read=", `"anchors":true`, path} {
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
	for _, want := range []string{"method [Widget]", "type Widget", "anchor_read=", `"anchors":true`, path} {
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
	for _, want := range []string{"method greet [Widget]", "impl Widget", "anchor_read=", `"anchors":true`, path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSyntaxRangeToolReceiptIncludesExactOldText(t *testing.T) {
	dir := t.TempDir()
	source := "package main\r\nfunc run() { println(\"🐾\") }\r\n"
	writeFile(t, dir, "main.go", source)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":1,"character":15}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	line := strings.Split(out, "\n")[0]
	field := strings.Split(line, "\treceipt=")
	if len(field) != 2 {
		t.Fatalf("missing structured receipt:\n%s", out)
	}
	var receipt struct {
		Offset int    `json:"offset"`
		Length int    `json:"length"`
		Hash   string `json:"hash"`
		Old    string `json:"old"`
	}
	if err := json.Unmarshal([]byte(field[1]), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Old != source[receipt.Offset:receipt.Offset+receipt.Length] {
		t.Fatalf("old text does not match receipt span: %#v", receipt)
	}
	sum := sha256.Sum256([]byte(receipt.Old))
	if got := hex.EncodeToString(sum[:])[:16]; got != receipt.Hash {
		t.Fatalf("hash = %q, want %q", receipt.Hash, got)
	}
}

func TestSyntaxRangeToolOmitsOversizedReceipt(t *testing.T) {
	dir := t.TempDir()
	source := "package main\n// " + strings.Repeat("x", maxSyntaxRangeReceiptBytes) + "\nfunc run() {}\n"
	writeFile(t, dir, "large.go", source)
	tool := &SyntaxRangeTool{Cwd: NewCwdRef(dir)}
	out, err := tool.Execute(context.Background(), `{"path":"large.go","line":2,"character":5}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "receipt_omitted=span_exceeds_") {
		t.Fatalf("expected oversized receipt marker:\n%s", out)
	}
	if len(out) > maxSyntaxRangeReceiptBytes {
		t.Fatalf("output grew with oversized source: %d bytes", len(out))
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
