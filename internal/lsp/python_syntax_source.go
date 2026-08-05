package lsp

import (
	"context"
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

// Ranges returns parser-backed enclosing ranges for a position in a Python
// file. It is intentionally structural (indentation-depth over a token
// stream, not full grammar) so it runs without a language server. Block
// headers may span multiple physical lines via an open paren/bracket/brace
// (Python's implicit line-continuation rule); pythonFrames tracks bracket
// depth so those still flatten into one logical line.
func (s pythonSyntaxSource) Ranges(ctx context.Context, path string, pos Position) ([]SyntaxRange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text, tokens, err := s.tokensFor(path)
	if err != nil {
		return nil, err
	}
	target, err := OffsetForPosition(text, pos)
	if err != nil {
		return nil, err
	}
	frames, err := pythonFrames(ctx, tokens)
	if err != nil {
		return nil, err
	}
	var ranges []SyntaxRange
	for _, f := range frames {
		if target < f.startOffset || target > f.endOffset {
			continue
		}
		start, err1 := PositionForOffset(text, f.startOffset)
		end, err2 := PositionForOffset(text, f.endOffset)
		if err1 != nil || err2 != nil {
			continue
		}
		ranges = append(ranges, SyntaxRange{Kind: f.kind, Name: f.name, Detail: containerOrDefault(f.detail), Range: TextRange{Start: start, End: end}})
	}
	if fileEnd, err := PositionForOffset(text, len(text)); err == nil {
		ranges = append(ranges, SyntaxRange{Kind: "file", Name: filepath.Base(path), Detail: "parser", Range: TextRange{Start: Position{}, End: fileEnd}})
	}
	return sortSyntaxRanges(dedupeSyntaxRanges(ranges)), nil
}

// pythonFrames walks the token stream once, grouping tokens into logical
// lines and tracking an indent stack. A later line whose indent is <= an
// open block's indent closes that block (and any more deeply nested ones);
// this mirrors Python's own off-side-rule block structure without building a
// full grammar.
func pythonFrames(ctx context.Context, tokens []chroma.Token) ([]pyFrame, error) {
	var frames []pyFrame
	var stack []pyFrame
	var lineTokens []chroma.Token
	var lineOffsets []int
	offset := 0
	lineStart := 0
	lastContentEnd := 0
	// bracketDepth tracks open (unmatched) (), [], {} — Python's implicit
	// line-continuation rule suspends logical-line splitting inside any of
	// them, e.g. a wrapped `def run(\n    a,\n    b,\n):` parameter list.
	// Runs for the whole file, not per-line: it only returns to 0 once every
	// opened bracket closes, which is exactly when line-splitting may resume.
	bracketDepth := 0
	// prevLineEnd is lastContentEnd as of the end of the previous non-blank
	// line — the correct close offset when a later line dedents past an open
	// block. Using the live lastContentEnd instead would extend the closing
	// block's range through the dedented line's own content, since
	// lastContentEnd is already updated with that line's tokens by the time
	// handleLine sees it.
	prevLineEnd := 0
	// atLineStart tracks whether we haven't yet seen real content since the
	// last physical newline — independent of bracketDepth/structural line
	// breaks, so the resync check below can tell "first token of a physical
	// line" even while still nominally inside an open bracket.
	atLineStart := true
	// physicalLineStart is the offset where the current physical line began
	// (before any leading whitespace), updated on every real newline
	// regardless of bracketDepth. Resync needs this rather than the resync
	// keyword token's own offset — indent is measured from the true start of
	// the line, not from wherever leading whitespace happened to end.
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
			return nil, err
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
	closeTo(0, lastContentEnd)
	return frames, nil
}
