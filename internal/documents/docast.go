package documents

import (
	"fmt"
	"strings"
)

// Block is one node of a DocAST block tree. Which fields apply depends on
// Type; unused fields are left zero. Deliberately mirrors Pandoc's own
// block shape (heading, paragraph, list, table, code) rather than
// inventing a new schema, since Pandoc is the generation backend for
// docx/pdf — see roadmap/document-generation.md.
type Block struct {
	// Type selects which other fields apply: "heading", "paragraph",
	// "list", "table", or "code".
	Type string

	// Level is the heading level (1-6). heading only.
	Level int

	// Text is the block's text content. heading, paragraph, and code.
	Text string

	// Ordered selects a numbered (true) vs bulleted (false) list. list only.
	Ordered bool

	// Items is the list's entries, one per line. list only.
	Items []string

	// Header is the table's column headers. table only.
	Header []string

	// Rows is the table's body, one slice of cells per row. table only.
	Rows [][]string

	// Language is the code block's fenced-block language tag (e.g. "go",
	// "python"); empty renders a plain fence. code only.
	Language string
}

// DocAST is the block-tree intermediate representation for docx/pdf
// generation — see roadmap/document-generation.md's "Two canonical
// intermediate representations". Rendered to Pandoc-flavored Markdown by
// RenderMarkdown, then handed to pandoc for the actual docx/pdf conversion.
type DocAST struct {
	Blocks []Block
}

// RenderMarkdown renders ast as Pandoc-flavored Markdown text. Every block's
// text content is escaped (see escapeMarkdown) so user-supplied content
// can't inject unintended Markdown structure (a paragraph starting with
// "# " becoming a heading, a table cell containing "|" splitting a
// column, etc).
func RenderMarkdown(ast DocAST) (string, error) {
	var b strings.Builder
	for i, block := range ast.Blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		if err := renderBlock(&b, block); err != nil {
			return "", fmt.Errorf("block %d: %w", i, err)
		}
	}
	return b.String(), nil
}

func renderBlock(b *strings.Builder, block Block) error {
	switch block.Type {
	case "heading":
		level := block.Level
		if level < 1 || level > 6 {
			level = 1
		}
		fmt.Fprintf(b, "%s %s\n", strings.Repeat("#", level), escapeMarkdown(block.Text))
	case "paragraph":
		fmt.Fprintf(b, "%s\n", escapeMarkdown(block.Text))
	case "list":
		for i, item := range block.Items {
			marker := "-"
			if block.Ordered {
				marker = fmt.Sprintf("%d.", i+1)
			}
			fmt.Fprintf(b, "%s %s\n", marker, escapeMarkdown(item))
		}
	case "table":
		if err := renderTable(b, block); err != nil {
			return err
		}
	case "code":
		renderCodeBlock(b, block)
	default:
		return fmt.Errorf("unknown block type %q", block.Type)
	}
	return nil
}

func renderCodeBlock(b *strings.Builder, block Block) {
	fence := strings.Repeat("`", longestBacktickRun(block.Text)+1)
	if len(fence) < 3 {
		fence = "```"
	}
	language := sanitizeCodeLanguage(block.Language)
	if language != "" {
		fmt.Fprintf(b, "%s%s\n%s\n%s\n", fence, language, block.Text, fence)
		return
	}
	fmt.Fprintf(b, "%s\n%s\n%s\n", fence, block.Text, fence)
}

func longestBacktickRun(s string) int {
	longest, current := 0, 0
	for _, r := range s {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	return longest
}

func sanitizeCodeLanguage(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return ""
	}
	s = fields[0]
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '+' || r == '#' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderTable(b *strings.Builder, block Block) error {
	if len(block.Header) == 0 {
		return fmt.Errorf("table block has no header")
	}
	writeRow := func(cells []string) {
		b.WriteString("|")
		for _, c := range cells {
			b.WriteString(" ")
			b.WriteString(escapeMarkdownTableCell(c))
			b.WriteString(" |")
		}
		b.WriteString("\n")
	}
	writeRow(block.Header)
	b.WriteString("|")
	for range block.Header {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, row := range block.Rows {
		if len(row) != len(block.Header) {
			return fmt.Errorf("table row has %d cells, header has %d", len(row), len(block.Header))
		}
		writeRow(row)
	}
	return nil
}

// escapeMarkdown escapes text that will sit inside a heading/paragraph/list
// item so it can't be read back as Markdown structure: a literal backslash
// or leading structural character (#, -, *, >, digit-dot) at the start of
// the escaped run would otherwise be interpreted by pandoc as a heading,
// list, blockquote, or ordered-list marker instead of the intended literal
// text.
func escapeMarkdown(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "]", "\\]")
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "-") || strings.HasPrefix(s, ">") {
		s = "\\" + s
	}
	return s
}

// escapeMarkdownTableCell escapes a table cell: everything escapeMarkdown
// does, plus pipe characters (which would otherwise split into an extra
// column) and newlines (which would break the one-line-per-row table
// syntax).
func escapeMarkdownTableCell(s string) string {
	s = escapeMarkdown(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
