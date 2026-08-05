package lsp

import (
	"context"
	"path/filepath"
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
)

func init() {
	RegisterSyntaxSymbolSource("typescript", braceSyntaxSource{spec: tsBraceSpec})
	RegisterSyntaxSymbolSource("rust", braceSyntaxSource{spec: rustBraceSpec})
}

// braceLanguageSpec parameterizes the shared brace-depth structural scanner
// for curly-brace languages. It stays data-only so TypeScript/JavaScript and
// Rust can share one walker instead of duplicating token-stream logic.
type braceLanguageSpec struct {
	// lexerFor resolves the chroma lexer name for one file, e.g. picking
	// "tsx" vs "typescript" from the real extension.
	lexerFor func(path string) string
	// blockKeywords maps a header-leading keyword token Value to the Kind
	// emitted for the brace it introduces.
	blockKeywords map[string]string
	// namedKinds are Kinds whose Name is the first Name-category token found
	// after the matched keyword (function/class/struct/... but not if/for/...).
	namedKinds map[string]bool
	// symbolKinds are Kinds worth reporting from Symbols() (declarations,
	// not control-flow blocks).
	symbolKinds map[string]bool
	// containerKinds are Kinds that make a nested function/fn a "method" and
	// lend their Name as the nested frame's Detail.
	containerKinds map[string]bool
	// functionKind is the Kind used for a plain function/fn declaration or
	// expression; methodKind (if non-empty) replaces it when the immediate
	// enclosing frame is a containerKind.
	functionKind string
	methodKind   string
	// arrowToken is the arrow-function token Value ("=>" for TS/JS); empty
	// disables arrow-function detection (Rust has no arrow functions, and its
	// match arms also end a header in "=>" so treating it as one would
	// mislabel every match arm as a function).
	arrowToken string
	// allowMethodShorthand recognizes ES6 method shorthand (`name() { }`
	// inside a class/object body, no leading keyword) — TypeScript/JavaScript
	// only; Rust has no equivalent (it always requires `fn`).
	allowMethodShorthand bool
	// objectPrecedents are token Values that, appearing as the last pending
	// token before an unlabeled '{', mark it as a value/object literal
	// rather than a bare block (TypeScript/JavaScript only; nil for Rust).
	objectPrecedents map[string]bool
}

// tsLexerFor always resolves to chroma's "javascript" lexer, deliberately
// never "typescript"/"tsx"/"jsx". chroma's typescript.xml grammar has a rule
// (`([a-zA-Z_?.$][\w?.$]*)\(\) \{`) that coalesces any zero-argument method
// header — e.g. `constructor() {` — into one NameOther blob token containing
// the opening brace, which silently breaks brace-depth tracking for exactly
// the most common method shape. javascript.xml has no such rule and already
// recognizes most TS/JSX keywords (class/interface/enum/extends/implements/
// private/public/static/async/...); unrecognized TS-only syntax (generics,
// type annotations, `interface`/`namespace` bodies) degrades to inert
// identifier/operator tokens without corrupting brace structure — verified
// by round-tripping token reconstruction against interfaces, generics, and
// JSX-with-braces fixtures.
func tsLexerFor(string) string {
	return "javascript"
}

var tsBraceSpec = braceLanguageSpec{
	lexerFor: tsLexerFor,
	blockKeywords: map[string]string{
		"function": "function", "class": "class", "interface": "interface",
		"enum": "enum", "namespace": "namespace", "module": "namespace",
		"if": "if", "else": "else", "for": "for", "while": "while", "do": "do",
		"switch": "switch", "try": "try", "catch": "catch", "finally": "finally",
	},
	namedKinds:           map[string]bool{"function": true, "class": true, "interface": true, "enum": true, "namespace": true},
	symbolKinds:          map[string]bool{"function": true, "method": true, "class": true, "interface": true, "enum": true, "namespace": true},
	containerKinds:       map[string]bool{"class": true, "interface": true},
	functionKind:         "function",
	methodKind:           "method",
	arrowToken:           "=>",
	allowMethodShorthand: true,
	objectPrecedents: map[string]bool{
		"=": true, "return": true, "(": true, ",": true, ":": true, "[": true,
	},
}

var rustBraceSpec = braceLanguageSpec{
	lexerFor: func(string) string { return "rust" },
	blockKeywords: map[string]string{
		"fn": "fn", "struct": "struct", "enum": "enum", "trait": "trait",
		"impl": "impl", "mod": "mod",
		"if": "if", "else": "else", "for": "for", "while": "while",
		"loop": "loop", "match": "match",
	},
	namedKinds:     map[string]bool{"fn": true, "struct": true, "enum": true, "trait": true, "mod": true},
	symbolKinds:    map[string]bool{"fn": true, "struct": true, "enum": true, "trait": true, "impl": true, "mod": true},
	containerKinds: map[string]bool{"impl": true, "trait": true},
	functionKind:   "fn",
	methodKind:     "",
}

// braceFrame is one resolved '{'...'}' span. Kind/Name/Detail are computed
// when the '{' is pushed (the header preceding it is fully known then);
// endOffset is filled in when the matching '}' pops it.
type braceFrame struct {
	kind        string
	name        string
	detail      string
	startOffset int
	nameOffset  int
	endOffset   int
}

type braceSyntaxSource struct {
	spec braceLanguageSpec
}

func (s braceSyntaxSource) tokensFor(path string) (string, []chroma.Token, error) {
	return chromaTokensForFile(path, s.spec.lexerFor(path))
}

func (s braceSyntaxSource) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text, tokens, err := s.tokensFor(path)
	if err != nil {
		return nil, err
	}
	frames, err := braceFrames(ctx, s.spec, tokens)
	if err != nil {
		return nil, err
	}
	var out []Symbol
	for _, f := range frames {
		if !s.spec.symbolKinds[f.kind] || f.name == "" {
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

func containerOrDefault(detail string) string {
	if detail == "" {
		return "parser"
	}
	return detail
}

// Ranges returns parser-backed enclosing ranges for a position in a
// brace-delimited language. It is intentionally structural (brace-depth,
// not full grammar) so it runs without a language server.
func (s braceSyntaxSource) Ranges(ctx context.Context, path string, pos Position) ([]SyntaxRange, error) {
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
	frames, err := braceFrames(ctx, s.spec, tokens)
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

// braceFrames walks the token stream once, tracking brace nesting and the
// "pending header" (tokens since the last statement/brace boundary) used to
// label each brace when it opens. Kind/Name/Detail are resolved at push time
// because the header is fully known by then; only endOffset waits for pop.
func braceFrames(ctx context.Context, spec braceLanguageSpec, tokens []chroma.Token) ([]braceFrame, error) {
	var frames []braceFrame
	var stack []braceFrame
	var pending []chroma.Token
	var pendingOffsets []int
	offset := 0
	for _, tok := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cat := tok.Type.Category()
		if cat == chroma.Comment {
			offset += len(tok.Value)
			continue
		}
		if cat == chroma.Text && strings.TrimSpace(tok.Value) == "" {
			offset += len(tok.Value)
			continue
		}
		if cat == chroma.Punctuation && (tok.Value == "{" || tok.Value == "}" || tok.Value == ";") {
			switch tok.Value {
			case "{":
				stack = append(stack, resolveBraceFrame(spec, pending, pendingOffsets, offset, stack))
			case "}":
				if n := len(stack); n > 0 {
					top := stack[n-1]
					stack = stack[:n-1]
					top.endOffset = offset + len(tok.Value)
					frames = append(frames, top)
				}
			}
			pending = pending[:0]
			pendingOffsets = pendingOffsets[:0]
			offset += len(tok.Value)
			continue
		}
		pending = append(pending, tok)
		pendingOffsets = append(pendingOffsets, offset)
		offset += len(tok.Value)
	}
	return frames, nil
}

func resolveBraceFrame(spec braceLanguageSpec, pending []chroma.Token, pendingOffsets []int, braceOffset int, stack []braceFrame) braceFrame {
	f := braceFrame{startOffset: braceOffset, nameOffset: braceOffset}
	if len(pending) > 0 {
		f.startOffset = pendingOffsets[0]
		f.nameOffset = pendingOffsets[0]
	}
	kwIdx := -1
	for i, tok := range pending {
		if kind, ok := spec.blockKeywords[tok.Value]; ok {
			f.kind = kind
			kwIdx = i
			break
		}
	}
	last := ""
	if len(pending) > 0 {
		last = pending[len(pending)-1].Value
	}
	switch {
	case kwIdx >= 0 && f.kind == "impl":
		f.name = rustImplName(pending[kwIdx+1:])
	case kwIdx >= 0 && spec.namedKinds[f.kind]:
		for j := kwIdx + 1; j < len(pending); j++ {
			if pending[j].Type.Category() == chroma.Name {
				f.name = pending[j].Value
				f.nameOffset = pendingOffsets[j]
				break
			}
		}
		if f.name == "" && f.kind == spec.functionKind {
			f.name, f.nameOffset = nameBeforeAssign(pending, pendingOffsets, kwIdx)
		}
	case kwIdx >= 0:
		// Control-flow keyword (if/for/while/switch/...); Kind is already
		// set from the scan above and these never carry a Name.
	case spec.arrowToken != "" && last == spec.arrowToken:
		f.kind = spec.functionKind
		f.name, f.nameOffset = nameBeforeAssign(pending, pendingOffsets, len(pending))
	case spec.allowMethodShorthand:
		if name, off, ok := methodShorthandName(pending, pendingOffsets); ok {
			f.kind = spec.functionKind
			f.name = name
			f.nameOffset = off
		} else if spec.objectPrecedents[last] {
			f.kind = "object"
		} else {
			f.kind = "block"
		}
	case spec.objectPrecedents[last]:
		f.kind = "object"
	default:
		f.kind = "block"
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		if spec.containerKinds[top.kind] {
			f.detail = top.name
			if f.kind == spec.functionKind && spec.methodKind != "" {
				f.kind = spec.methodKind
			}
		}
	}
	return f
}

// nameBeforeAssign looks backward from index `before` in pending for a
// `NAME =` pattern, covering `const foo = function() {}` and
// `const foo = () => {}`.
func nameBeforeAssign(pending []chroma.Token, pendingOffsets []int, before int) (string, int) {
	for j := before - 1; j > 0; j-- {
		if pending[j].Value == "=" && pending[j-1].Type.Category() == chroma.Name {
			return pending[j-1].Value, pendingOffsets[j-1]
		}
	}
	return "", 0
}

// methodShorthandName recognizes ES6 method shorthand — `name(...) { }` with
// no leading `function` keyword, the form every TypeScript/JavaScript class
// or object-literal method actually uses. The name is whatever Name-category
// token sits immediately before the first '(' in the header (skipping
// modifiers like `async`/`static`/`private` that precede it).
func methodShorthandName(pending []chroma.Token, pendingOffsets []int) (string, int, bool) {
	for i, tok := range pending {
		if tok.Value == "(" {
			if i > 0 && pending[i-1].Type.Category() == chroma.Name {
				return pending[i-1].Value, pendingOffsets[i-1], true
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

// rustImplName extracts "Type" or "Trait for Type" from the tokens after the
// "impl" keyword, skipping generic parameter lists (tracked via angle-bracket
// depth) so `impl<T: Display> Trait for Foo<T>` yields "Trait for Foo".
func rustImplName(tokens []chroma.Token) string {
	depth := 0
	var parts []string
	for _, tok := range tokens {
		switch tok.Value {
		case "<":
			depth++
			continue
		case ">":
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		if tok.Type.Category() == chroma.Name || tok.Value == "for" {
			parts = append(parts, tok.Value)
		}
	}
	return strings.Join(parts, " ")
}
