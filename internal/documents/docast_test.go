package documents

import (
	"strings"
	"testing"
)

func TestRenderMarkdownHeadingLevels(t *testing.T) {
	ast := DocAST{Blocks: []Block{
		{Type: "heading", Level: 1, Text: "Title"},
		{Type: "heading", Level: 3, Text: "Subsection"},
		{Type: "heading", Level: 9, Text: "Clamped"},
	}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	for _, want := range []string{"# Title", "### Subsection", "# Clamped"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderMarkdownParagraph(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "paragraph", Text: "Hello world."}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "Hello world.") {
		t.Errorf("expected paragraph text, got:\n%s", got)
	}
}

func TestRenderMarkdownListOrderedAndUnordered(t *testing.T) {
	ast := DocAST{Blocks: []Block{
		{Type: "list", Ordered: false, Items: []string{"apple", "banana"}},
		{Type: "list", Ordered: true, Items: []string{"first", "second"}},
	}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	for _, want := range []string{"- apple", "- banana", "1. first", "2. second"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderMarkdownTable(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type:   "table",
		Header: []string{"Name", "Qty"},
		Rows: [][]string{
			{"Widget", "3"},
			{"Gadget", "5"},
		},
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	for _, want := range []string{"| Name | Qty |", "| --- | --- |", "| Widget | 3 |", "| Gadget | 5 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderMarkdownTableEscapesSpecialCharacters(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type:   "table",
		Header: []string{"Expr"},
		Rows: [][]string{
			{"a|b\nc"},
		},
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Contains(got, "a|b") {
		t.Errorf("expected pipe to be escaped, got:\n%s", got)
	}
	if !strings.Contains(got, `a\|b c`) {
		t.Errorf("expected escaped pipe and collapsed newline, got:\n%s", got)
	}
}

func TestRenderMarkdownTableMismatchedRowLength(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type:   "table",
		Header: []string{"A", "B"},
		Rows:   [][]string{{"only one"}},
	}}}
	if _, err := RenderMarkdown(ast); err == nil {
		t.Fatal("expected an error for a row with the wrong number of cells")
	}
}

func TestRenderMarkdownTableRequiresHeader(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "table", Rows: [][]string{{"x"}}}}}
	if _, err := RenderMarkdown(ast); err == nil {
		t.Fatal("expected an error for a table with no header")
	}
}

func TestRenderMarkdownCodeBlockWithLanguage(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "code", Language: "go", Text: "func main() {}"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "```go\nfunc main() {}\n```") {
		t.Errorf("expected fenced code block with language tag, got:\n%s", got)
	}
}

func TestRenderMarkdownCodeBlockDoesNotEscapeContent(t *testing.T) {
	// Code block text is verbatim inside a fence; escapeMarkdown must not
	// mangle it (e.g. turning `*args` into `\*args`).
	ast := DocAST{Blocks: []Block{{Type: "code", Text: "def f(*args): pass"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "def f(*args): pass") {
		t.Errorf("expected verbatim code text, got:\n%s", got)
	}
}

func TestRenderMarkdownCodeBlockFenceBeatsContentBackticks(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "code", Language: "go", Text: "fmt.Println(```danger```)"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "````go\nfmt.Println(```danger```)\n````") {
		t.Errorf("expected a fence longer than the code content's backtick run, got:\n%s", got)
	}
}

func TestRenderMarkdownCodeBlockSanitizesLanguage(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "code", Language: "go {.evil}\n# heading", Text: "x"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Contains(got, "{") || strings.Contains(got, "heading") || !strings.Contains(got, "```go\nx\n```") {
		t.Errorf("expected sanitized language tag, got:\n%s", got)
	}
}

func TestRenderMarkdownUnknownBlockType(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "image"}}}
	if _, err := RenderMarkdown(ast); err == nil {
		t.Fatal("expected an error for an unsupported block type")
	}
}

func TestEscapeMarkdownPreventsStructuralInjection(t *testing.T) {
	cases := map[string]string{
		"# not a heading": `\# not a heading`,
		"- not a list":    `\- not a list`,
		"> not a quote":   `\> not a quote`,
		"*bold*":          `\*bold\*`,
		"under_score":     `under\_score`,
		"[link](evil)":    `\[link\](evil)`,
		`back\slash`:      `back\\slash`,
		"`code span`":     "\\`code span\\`",
	}
	for in, want := range cases {
		got := escapeMarkdown(in)
		if got != want {
			t.Errorf("escapeMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}
