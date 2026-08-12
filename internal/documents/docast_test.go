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
	ast := DocAST{Blocks: []Block{{Type: "video"}}}
	if _, err := RenderMarkdown(ast); err == nil {
		t.Fatal("expected an error for an unsupported block type")
	}
}

func TestEscapeMarkdownPreventsStructuralInjection(t *testing.T) {
	cases := map[string]string{
		"# not a heading":   `\# not a heading`,
		"- not a list":      `\- not a list`,
		"> not a quote":     `\> not a quote`,
		"+ not a bullet":    `\+ not a bullet`,
		"1. not a list":     `1\. not a list`,
		"12) not a list":    `12\) not a list`,
		"1.5 is not a list": `1\.5 is not a list`,
		"*bold*":            `\*bold\*`,
		"under_score":       `under\_score`,
		"[link](evil)":      `\[link\](evil)`,
		`back\slash`:        `back\\slash`,
		"`code span`":       "\\`code span\\`",
		`<img src="x">`:     `\<img src="x">`,
	}
	for in, want := range cases {
		got := escapeMarkdown(in)
		if got != want {
			t.Errorf("escapeMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeMarkdownCollapsesEmbeddedNewlines(t *testing.T) {
	got := escapeMarkdown("Intro line\n\n# INJECTED HEADING\n\nMore text")
	if strings.Contains(got, "\n") {
		t.Errorf("expected no embedded newlines, got %q", got)
	}
	// No leading-marker backslash is needed here: once collapsed onto one
	// line, "# INJECTED HEADING" is no longer the first token on its own
	// line, so ATX heading syntax (which requires '#' to start the line)
	// can't fire regardless — the newline collapse alone is the fix.
	want := "Intro line  # INJECTED HEADING  More text"
	if got != want {
		t.Errorf("escapeMarkdown collapsed newlines unexpectedly: got %q, want %q", got, want)
	}
}

// TestRenderMarkdownParagraphCannotInjectHTMLOrBlocks is the end-to-end
// regression for the two security findings: a paragraph whose Text
// contains raw HTML or embedded blank-line-plus-heading must render as
// one literal, harmless line — not a live <img> tag or a real ATX
// heading block.
func TestRenderMarkdownParagraphCannotInjectHTMLOrBlocks(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type: "paragraph",
		Text: "See <img src=\"http://attacker.example/x.png\"> then\n\n# INJECTED\n\nmore",
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, `\<img`) {
		t.Errorf("expected the '<' to be backslash-escaped ('\\<img'), got %q", got)
	}
	// Exactly one block: a single paragraph line, not multiple blocks
	// separated by blank lines.
	if strings.Count(strings.TrimSpace(got), "\n\n") != 0 {
		t.Errorf("expected a single paragraph, got multiple blocks:\n%s", got)
	}
	if strings.Contains(got, "\n# ") {
		t.Errorf("embedded text was still interpretable as a real heading:\n%s", got)
	}
}

func TestRenderMarkdownSpansBoldItalic(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type: "paragraph",
		Spans: []Span{
			{Text: "plain "},
			{Text: "bold", Bold: true},
			{Text: " "},
			{Text: "italic", Italic: true},
			{Text: " "},
			{Text: "both", Bold: true, Italic: true},
		},
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	want := "plain **bold** _italic_ ***both***\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderMarkdownSpansFallBackToText(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "paragraph", Text: "plain text, no spans"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "plain text, no spans") {
		t.Errorf("expected fallback to Text when Spans is empty, got %q", got)
	}
}

func TestRenderMarkdownSpansStillEscapeContent(t *testing.T) {
	// A malicious span's Text must still be escaped even though it's
	// wrapped in real, unescaped emphasis markers — the wrapper is
	// structural output this package controls; the content inside it
	// is untrusted and must go through the same defense as plain Text.
	ast := DocAST{Blocks: []Block{{
		Type:  "paragraph",
		Spans: []Span{{Text: "<img src=\"http://evil\">", Bold: true}},
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, `\<img`) {
		t.Errorf("expected span content to still be HTML-escaped, got %q", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "**") {
		t.Errorf("expected the bold wrapper markers themselves to stay unescaped, got %q", got)
	}
}

func TestRenderMarkdownListItemSpans(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type:      "list",
		Items:     []string{"plain item", "spans item"},
		ItemSpans: [][]Span{nil, {{Text: "bold item", Bold: true}}},
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, "- plain item") {
		t.Errorf("expected first item to fall back to plain Items text, got %q", got)
	}
	if !strings.Contains(got, "- **bold item**") {
		t.Errorf("expected second item to use ItemSpans, got %q", got)
	}
}

func TestRenderMarkdownImageBlock(t *testing.T) {
	ast := DocAST{Blocks: []Block{{
		Type: "image",
		Path: "/work/assets/logo.png",
		Alt:  "Company logo",
	}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	want := "![Company logo](/work/assets/logo.png)\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderMarkdownImageAltIsEscaped(t *testing.T) {
	ast := DocAST{Blocks: []Block{{Type: "image", Path: "/x.png", Alt: "] injected"}}}
	got, err := RenderMarkdown(ast)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(got, `\] injected`) {
		t.Errorf("expected alt text ']' to be escaped, got %q", got)
	}
}
