package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
)

func init() {
	RegisterSyntaxSymbolSource("python", pythonSyntaxSource{})
}

// pyBlockKeywords maps a line-leading Python keyword to the Kind emitted for
// the indented block it introduces. "async def"/"async for"/"async with" are
// handled by peeking past a leading "async" token, not listed here.
var pyBlockKeywords = map[string]string{
	"def": "function", "class": "class",
	"if": "if", "elif": "elif", "else": "else",
	"for": "for", "while": "while",
	"try": "try", "except": "except", "finally": "finally",
	"with": "with",
}

var pySymbolKinds = map[string]bool{"function": true, "method": true, "class": true}

// pyResyncKeywords are pyBlockKeywords entries that can never legally appear
// as a token inside an open (), [], or {} — Python has no def-expression,
// class-expression, try-expression, except/finally clause, with-expression,
// while-expression, or bare elif outside an if-statement. If one shows up as
// the first token on a physical line while bracketDepth is still > 0, the
// depth count has necessarily drifted (an unclosed bracket upstream, e.g.
// malformed or mid-edit source) rather than the source legitimately
// continuing an expression, so it's safe to resynchronize. `if`/`else`/`for`
// are deliberately excluded: they're valid inside ternaries and
// comprehensions, so seeing one bracket-nested is not a reliable signal.
var pyResyncKeywords = map[string]bool{
	"def": true, "class": true, "elif": true,
	"try": true, "except": true, "finally": true,
	"with": true, "while": true,
}

// pyFrame is one indentation-delimited block. Kind/Name/Detail/indent are
// resolved when the block's header line is scanned; endOffset waits until a
// later line dedents past it (or EOF).
type pyFrame struct {
	kind        string
	name        string
	detail      string
	indent      int
	startOffset int
	nameOffset  int
	endOffset   int
}

type pythonSyntaxSource struct{}

func (pythonSyntaxSource) SyntaxMode() string { return "scanner" }

func (pythonSyntaxSource) tokensFor(path string) (string, []chroma.Token, error) {
	return chromaTokensForFile(path, "python")
}

func (s pythonSyntaxSource) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text, tokens, err := s.tokensFor(path)
	if err != nil {
		return nil, err
	}
	frames, err := pythonFrames(ctx, tokens)
	if err != nil {
		return nil, err
	}
	var out []Symbol
	for _, f := range frames {
		if !pySymbolKinds[f.kind] || f.name == "" {
			continue
		}
		loc, err := PositionForOffset(text, f.nameOffset)
		if err != nil {
			continue
		}
		start, err1 := PositionForOffset(text, f.startOffset)
		end, err2 := PositionForOffset(text, f.endOffset)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Symbol{
			Name: f.name, Kind: f.kind, Container: containerOrDefault(f.detail),
			Location: Location{Path: path, Line: loc.Line, Character: loc.Character},
			Range:    TextRange{Start: start, End: end},
		})
	}
	return out, nil
}

// Ranges returns enclosing ranges for callers that do not need the immutable
// source snapshot. It delegates to the same scanner path used by syntax_range.
func (s pythonSyntaxSource) Ranges(ctx context.Context, path string, pos Position) ([]SyntaxRange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ranges, _, err := s.RangesFromSource(ctx, path, src, pos)
	return ranges, err
}

func (s pythonSyntaxSource) RangesFromSource(ctx context.Context, path string, src []byte, pos Position) ([]SyntaxRange, []string, error) {
	text, tokens, err := chromaTokensForSource(src, "python")
	if err != nil {
		return nil, nil, err
	}
	target, err := OffsetForPosition(text, pos)
	if err != nil {
		return nil, nil, err
	}
	frames, frameWarnings, err := pythonFramesWithWarnings(ctx, tokens)
	if err != nil {
		return nil, nil, err
	}
	var ranges []SyntaxRange
	for _, f := range frames {
		if byteContains(f.startOffset, f.endOffset, target) {
			kind := SyntaxKind(f.kind)
			if kind == "class" {
				kind = SyntaxKindType
			}
			if kind == "fn" {
				kind = SyntaxKindFunction
			}
			if item, ok := syntaxRangeFromBytes(text, kind, f.name, containerOrDefault(f.detail), f.startOffset, f.endOffset); ok {
				ranges = append(ranges, item)
			}
		}
	}
	if item, ok := syntaxRangeFromBytes(text, SyntaxKindFile, filepath.Base(path), "scanner", 0, len(src)); ok {
		ranges = append(ranges, item)
	}
	extra, warnings, err := pythonTokenRanges(ctx, text, tokens, target, frames)
	if err != nil {
		return nil, nil, err
	}
	ranges = append(ranges, extra...)
	warnings = append(frameWarnings, warnings...)
	return ranges, warnings, nil
}
func pythonTokenRanges(ctx context.Context, text string, tokens []chroma.Token, target int, frames []pyFrame) ([]SyntaxRange, []string, error) {
	flat := offsetTokens(tokens)
	var ranges []SyntaxRange
	var warnings []string
	for i, item := range flat {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if item.tok.Type == chroma.Error && len(warnings) == 0 {
			warnings = append(warnings, "scanner encountered unrecognized syntax; uncertain ranges were omitted")
		}
		if item.tok.Value == "(" && pythonCallCandidate(flat, i) {
			endIndex, ok := matchingDelimiterIndex(flat, i, "(", ")")
			if !ok {
				warnings = append(warnings, "scanner found unclosed call delimiter; incomplete call was omitted")
				continue
			}
			startIndex := calleeStartIndex(flat, i-1)
			start, end := flat[startIndex].start, flat[endIndex].end
			if byteContains(start, end, target) {
				name := strings.TrimSpace(text[start:flat[i].start])
				if r, ok := syntaxRangeFromBytes(text, SyntaxKindCall, name, "scanner", start, end); ok {
					ranges = append(ranges, r)
				}
			}
		}
		if (item.tok.Value == "import" || item.tok.Value == "from") && pythonStatementStart(text, item.start) {
			end, ok := pythonStatementEndTokens(text, flat, i)
			if !ok {
				warnings = append(warnings, "scanner found incomplete import declaration; range was omitted")
				continue
			}
			if byteContains(item.start, end, target) {
				if r, ok := syntaxRangeFromBytes(text, SyntaxKindImportBlock, "", "scanner", item.start, end); ok {
					ranges = append(ranges, r)
				}
			}
		}
	}
	// Class-suite assignments are fields only when directly indented under the
	// class, never when nested in a method or another block.
	for _, class := range frames {
		if class.kind != "class" || !byteContains(class.startOffset, class.endOffset, target) {
			continue
		}
		lineStart := class.startOffset
		for lineStart < class.endOffset {
			nl := strings.IndexByte(text[lineStart:class.endOffset], '\n')
			lineEnd := class.endOffset
			if nl >= 0 {
				lineEnd = lineStart + nl
			}
			line := text[lineStart:lineEnd]
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			if indent > class.indent && !pythonInsideNestedFrame(frames, class, lineStart) {
				name, ok := pythonFieldName(flat, lineStart+indent, lineEnd)
				if ok && byteContains(lineStart+indent, lineEnd, target) {
					if r, ok := syntaxRangeFromBytes(text, SyntaxKindField, name, "scanner", lineStart+indent, lineEnd); ok {
						ranges = append(ranges, r)
					}
				}
			}
			if nl < 0 {
				break
			}
			lineStart = lineEnd + 1
		}
	}
	return ranges, warnings, nil
}
func pythonInsideNestedFrame(frames []pyFrame, class pyFrame, offset int) bool {
	for _, frame := range frames {
		if frame.startOffset != class.startOffset && frame.startOffset >= class.startOffset && frame.endOffset <= class.endOffset && byteContains(frame.startOffset, frame.endOffset, offset) {
			return true
		}
	}
	return false
}

func pythonStatementStart(text string, offset int) bool {
	line := strings.LastIndex(text[:offset], "\n") + 1
	return strings.TrimSpace(text[line:offset]) == ""
}

func pythonCallCandidate(tokens []offsetToken, i int) bool {
	if i <= 0 || tokens[i-1].tok.Type.Category() != chroma.Name {
		return false
	}
	for j := i - 2; j >= 0; j-- {
		switch tokens[j].tok.Value {
		case ";", ":", "=", "return", "yield", ",":
			return true
		case "def", "class", "if", "for", "while", "with", "except":
			return false
		}
	}
	return true
}

func pythonStatementEndTokens(text string, tokens []offsetToken, start int) (int, bool) {
	depth := 0
	for i := start + 1; i < len(tokens); i++ {
		switch tokens[i].tok.Value {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return 0, false
			}
			depth--
		case ";":
			if depth == 0 {
				return tokens[i].end, true
			}
		}
		gap := text[tokens[i-1].end:tokens[i].start]
		if depth == 0 && strings.Contains(gap, "\n") && !strings.HasSuffix(strings.TrimRight(text[:tokens[i].start], " \t\r\n"), "\\") {
			return tokens[i-1].end, true
		}
	}
	if depth == 0 && start+1 < len(tokens) {
		return tokens[len(tokens)-1].end, true
	}
	return 0, false
}

func pythonFieldName(tokens []offsetToken, start, end int) (string, bool) {
	var line []offsetToken
	for _, item := range tokens {
		if item.start >= start && item.end <= end {
			line = append(line, item)
		}
	}
	if len(line) < 2 || line[0].tok.Type.Category() != chroma.Name {
		return "", false
	}
	for _, item := range line[1:] {
		switch item.tok.Value {
		case "=":
			return line[0].tok.Value, true
		case "(", ")", ".", ",":
			return "", false
		}
	}
	return "", false
}

// pythonFrames walks the token stream once, grouping tokens into logical
// lines and tracking an indent stack. A later line whose indent is <= an
// open block's indent closes that block.
func pythonFrames(ctx context.Context, tokens []chroma.Token) ([]pyFrame, error) {
	frames, _, err := pythonFramesWithWarnings(ctx, tokens)
	return frames, err
}

func pythonFramesWithWarnings(ctx context.Context, tokens []chroma.Token) ([]pyFrame, []string, error) {
	var frames []pyFrame
	var stack []pyFrame
	var lineTokens []chroma.Token
	var lineOffsets []int
	offset := 0
	lineStart := 0
	lastContentEnd := 0
	bracketDepth := 0
	var warnings []string
	prevLineEnd := 0
	atLineStart := true
	physicalLineStart := 0

	closeTo := func(indent, endOffset int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			top.endOffset = endOffset
			frames = append(frames, top)
		}
	}
	handleLine := func() {
		defer func() {
			lineTokens = lineTokens[:0]
			lineOffsets = lineOffsets[:0]
			prevLineEnd = lastContentEnd
		}()
		if len(lineTokens) == 0 {
			return
		}
		indent := lineOffsets[0] - lineStart
		closeTo(indent, prevLineEnd)
		startIdx := 0
		kw := lineTokens[0].Value
		if kw == "async" && len(lineTokens) > 1 {
			startIdx = 1
			kw = lineTokens[1].Value
		}
		kind, ok := pyBlockKeywords[kw]
		if !ok {
			return
		}
		if lineTokens[len(lineTokens)-1].Value != ":" {
			return
		}
		name, nameOffset := "", lineOffsets[0]
		if kind == "function" || kind == "class" {
			for j := startIdx + 1; j < len(lineTokens); j++ {
				if lineTokens[j].Type.Category() == chroma.Name {
					name = lineTokens[j].Value
					nameOffset = lineOffsets[j]
					break
				}
			}
		}
		detail := ""
		if n := len(stack); n > 0 && stack[n-1].kind == "class" && kind == "function" {
			detail = stack[n-1].name
			kind = "method"
		}
		stack = append(stack, pyFrame{kind: kind, name: name, detail: detail, indent: indent, startOffset: lineOffsets[0], nameOffset: nameOffset})
	}

	for _, tok := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		cat := tok.Type.Category()
		isBlank := cat == chroma.Text && strings.TrimSpace(tok.Value) == ""

		// A Text-category token containing '\n' always crosses a physical
		// line, but it's only a *structural* (logical) line break outside any
		// open bracket — inside one, Python treats the newline as
		// insignificant whitespace and the logical line continues. Multi-line
		// strings/docstrings tokenize per physical line with Category() ==
		// Literal, including their internal newlines, so those never reach
		// here at all — the whole string accumulates as one line's content
		// until the real trailing Text '\n' after its closing quote.
		if cat == chroma.Text && strings.Contains(tok.Value, "\n") {
			atLineStart = true
			lastNL := strings.LastIndex(tok.Value, "\n")
			offset += len(tok.Value)
			physicalLineStart = offset - (len(tok.Value) - lastNL - 1)
			if bracketDepth == 0 {
				handleLine()
				lineStart = physicalLineStart
			}
			continue
		}

		if !isBlank && cat != chroma.Comment {
			// Resync: a pure statement keyword can never legally appear
			// inside an open bracket, so seeing one as the first token of a
			// physical line while bracketDepth is still > 0 means the count
			// has drifted (an unclosed bracket somewhere upstream) rather
			// than a legitimately continuing expression. Recover instead of
			// silently losing the rest of the file to a single local error:
			// flush (and discard — it won't satisfy the trailing ':' check)
			// whatever had accumulated in the phantom line, and start fresh
			// as if this keyword began a new logical line at depth 0.
			if bracketDepth > 0 && atLineStart && cat == chroma.Keyword && pyResyncKeywords[tok.Value] {
				handleLine()
				warnings = append(warnings, "scanner recovered after an unclosed bracket; incomplete ranges were omitted")
				bracketDepth = 0
				lineStart = physicalLineStart
			}
			atLineStart = false
			lineTokens = append(lineTokens, tok)
			lineOffsets = append(lineOffsets, offset)
			if cat == chroma.Punctuation {
				switch tok.Value {
				case "(", "[", "{":
					bracketDepth++
				case ")", "]", "}":
					if bracketDepth > 0 {
						bracketDepth--
					}
				}
			}
		}
		if !isBlank {
			lastContentEnd = offset + len(tok.Value)
		}
		offset += len(tok.Value)
	}
	handleLine()
	if bracketDepth != 0 {
		warnings = append(warnings, "scanner found an unclosed bracket at end of file; incomplete ranges were omitted")
	}
	closeTo(0, lastContentEnd)
	return frames, warnings, nil
}
