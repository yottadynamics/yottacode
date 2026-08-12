package documents

import (
	"fmt"
	"strings"
)

// Block is one node of a DocAST block tree. Which fields apply depends on
// Type; unused fields are left zero. Deliberately mirrors Pandoc's own
// block shape (heading, paragraph, list, table, code, image) rather than
// inventing a new schema, since Pandoc is the generation backend for
// docx/pdf — see roadmap/document-generation.md.
type Block struct {
	// Type selects which other fields apply: "heading", "paragraph",
	// "list", "table", "code", or "image".
	Type string

	// Level is the heading level (1-6). heading only.
	Level int

	// Text is the block's plain text content. heading, paragraph, and
	// code. For heading/paragraph, Spans takes precedence when non-empty
	// — Text is the fallback plain-text path.
	Text string

	// Spans, when non-empty, renders inline-formatted text (bold/italic
	// runs) instead of Text. heading and paragraph only. Structured
	// spans rather than an inline-markdown string: each span's Text
	// still passes through escapeMarkdown, so a model can never smuggle
	// real Markdown/HTML syntax through a "formatting" field the way it
	// could through a raw markup string — see escapeMarkdown's own doc
	// comment for why that distinction matters.
	Spans []Span

	// Ordered selects a numbered (true) vs bulleted (false) list. list only.
	Ordered bool

	// Items is the list's plain-text entries, one per line. list only.
	// When ItemSpans is non-empty, it takes precedence per item.
	Items []string

	// ItemSpans, when non-empty, gives each list item inline formatting
	// the same way Spans does for heading/paragraph text. list only.
	// Must have the same length as Items when both are set; a shorter
	// ItemSpans falls back to the corresponding Items entry for the
	// missing tail.
	ItemSpans [][]Span

	// Header is the table's column headers. table only.
	Header []string

	// Rows is the table's body, one slice of cells per row. table only.
	Rows [][]string

	// Language is the code block's fenced-block language tag (e.g. "go",
	// "python"); empty renders a plain fence. code only.
	Language string

	// Path is the image's already-resolved, already-trust-validated
	// absolute file path. image only. Like ExtractRequest.Path, this
	// package never validates it — that trust boundary lives at the
	// agent-tool layer (see CreateDocumentTool.DenyReadPaths), the same
	// pattern read_document's Path field already documents.
	Path string

	// Alt is the image's alt text. image only.
	Alt string
}

// Span is one inline-formatted run of text within a heading, paragraph,
// or list item. See Block.Spans.
type Span struct {
	Text         string
	Bold, Italic bool
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
		fmt.Fprintf(b, "%s %s\n", strings.Repeat("#", level), renderInline(block.Spans, block.Text))
	case "paragraph":
		fmt.Fprintf(b, "%s\n", renderInline(block.Spans, block.Text))
	case "list":
		for i, item := range block.Items {
			marker := "-"
			if block.Ordered {
				marker = fmt.Sprintf("%d.", i+1)
			}
			var spans []Span
			if i < len(block.ItemSpans) {
				spans = block.ItemSpans[i]
			}
			fmt.Fprintf(b, "%s %s\n", marker, renderInline(spans, item))
		}
	case "table":
		if err := renderTable(b, block); err != nil {
			return err
		}
	case "code":
		renderCodeBlock(b, block)
	case "image":
		fmt.Fprintf(b, "![%s](%s)\n", escapeMarkdown(block.Alt), block.Path)
	default:
		return fmt.Errorf("unknown block type %q", block.Type)
	}
	return nil
}

// renderInline renders spans if non-empty (structured inline formatting),
// falling back to plain escaped text otherwise. See Block.Spans.
func renderInline(spans []Span, fallbackText string) string {
	if len(spans) == 0 {
		return escapeMarkdown(fallbackText)
	}
	var b strings.Builder
	for _, s := range spans {
		text := escapeMarkdown(s.Text)
		switch {
		case s.Bold && s.Italic:
			text = "***" + text + "***"
		case s.Bold:
			text = "**" + text + "**"
		case s.Italic:
			text = "_" + text + "_"
		}
		b.WriteString(text)
	}
	return b.String()
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
// item so it can't be read back as Markdown structure or raw HTML:
//   - a literal backslash, backtick, *, _, [, ] anywhere is escaped so it
//     can't open emphasis/code-span/link syntax;
//   - a literal '<' anywhere is escaped so text can't smuggle a raw HTML
//     tag into the Markdown pandoc hands to weasyprint for the pdf path
//     (weasyprint renders HTML/CSS, so an unescaped "<img src=...>" is a
//     live SSRF/exfiltration channel, not just a formatting quirk);
//   - an embedded newline is collapsed to a space, so a blank line inside
//     what's meant to be one literal block of text can't start a new
//     Markdown block (heading, list, blockquote) partway through — same
//     reasoning escapeMarkdownTableCell already applied to table cells;
//   - a leading structural marker (#, -, *, >, +, or an ordered-list
//     marker like "1." or "1)") is escaped so the run can't be read as a
//     heading, list, blockquote, or ordered-list item instead of the
//     intended literal text.
func escapeMarkdown(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "]", "\\]")
	s = strings.ReplaceAll(s, "<", "\\<")
	s = strings.ReplaceAll(s, "\n", " ")
	// '>' is deliberately left untouched by the ReplaceAll pass above and
	// checked here instead: escaping every '>' would also rewrite a
	// genuine leading '>' into "\>" before this prefix check ever saw it,
	// so the check below would never fire. Bare '>' elsewhere in the text
	// isn't itself dangerous (blockquote markers only trigger at the
	// start of a line), so leaving it unescaped except at the very start
	// is correct, not an oversight.
	switch {
	case strings.HasPrefix(s, "#"), strings.HasPrefix(s, "-"), strings.HasPrefix(s, ">"), strings.HasPrefix(s, "+"):
		s = "\\" + s
	default:
		if i := leadingOrderedListDelimiterIndex(s); i >= 0 {
			// Escape the '.'/')' delimiter itself, not the digits before
			// it — digits aren't ASCII punctuation, so CommonMark can't
			// backslash-escape them inline (a leading "\1" would render
			// with a stray literal backslash). Escaping the delimiter is
			// a real CommonMark escape and disappears in the output.
			s = s[:i] + "\\" + s[i:]
		}
	}
	return s
}

// leadingOrderedListDelimiterIndex returns the index of the '.' or ')'
// that would make s's opening one-to-nine digits read as a CommonMark
// ordered-list marker (e.g. "1. " or "12) "), or -1 if s doesn't start
// with a digit run followed by one of those delimiters. The trailing
// space CommonMark also requires for an actual list item isn't checked;
// treating a marker-shaped prefix as one even without it is over-
// cautious, not incorrect — an unnecessary escape is harmless.
func leadingOrderedListDelimiterIndex(s string) int {
	i := 0
	for i < len(s) && i < 9 && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return -1
	}
	if s[i] == '.' || s[i] == ')' {
		return i
	}
	return -1
}

// escapeMarkdownTableCell escapes a table cell: everything escapeMarkdown
// does, plus pipe characters (which would otherwise split into an extra
// column). escapeMarkdown already collapses embedded newlines to spaces.
func escapeMarkdownTableCell(s string) string {
	s = escapeMarkdown(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
