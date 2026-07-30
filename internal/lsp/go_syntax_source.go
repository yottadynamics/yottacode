package lsp

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
)

func init() {
	RegisterSyntaxSymbolSource("go", goSyntaxSource{})
}

type goSyntaxSource struct{}

func (goSyntaxSource) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil && file == nil {
		return nil, err
	}
	text := string(src)
	var out []Symbol
	for _, decl := range file.Decls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			out = append(out, goFuncSymbol(path, text, fset, d))
		case *ast.GenDecl:
			out = append(out, goGenSymbols(path, text, fset, d)...)
		}
	}
	return out, nil
}

// Ranges returns parser-backed enclosing ranges for a Go source position. It is
// intentionally local and syntactic so it can run without a gopls process.
func (goSyntaxSource) Ranges(ctx context.Context, path string, pos Position) ([]SyntaxRange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(src)
	offset, err := OffsetForPosition(text, pos)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil && file == nil {
		return nil, err
	}
	tokFile := fset.File(file.Pos())
	if tokFile == nil {
		return nil, nil
	}
	target := tokFile.Pos(offset)
	var ranges []SyntaxRange
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if err := ctx.Err(); err != nil {
			return false
		}
		if !tokenContains(n.Pos(), n.End(), target) {
			return true
		}
		if item, ok := goSyntaxRangeForNode(text, fset, n); ok {
			ranges = append(ranges, item)
		}
		return true
	})
	if file.Pos().IsValid() && file.End().IsValid() {
		ranges = append(ranges, SyntaxRange{Kind: "file", Name: file.Name.Name, Detail: "parser", Range: goRange(text, fset, file.Pos(), file.End())})
	}
	return sortSyntaxRanges(dedupeSyntaxRanges(ranges)), nil
}

func goFuncSymbol(path, text string, fset *token.FileSet, d *ast.FuncDecl) Symbol {
	kind := "function"
	container := "parser"
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = "method"
		container = goReceiverName(d.Recv.List[0].Type)
	}
	return Symbol{Name: d.Name.Name, Kind: kind, Container: container, Location: goLocation(path, text, fset, d.Name.Pos()), Range: goRange(text, fset, d.Pos(), d.End())}
}

func goGenSymbols(path, text string, fset *token.FileSet, d *ast.GenDecl) []Symbol {
	kind := goDeclKind(d.Tok)
	if kind == "" {
		return nil
	}
	var out []Symbol
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			out = append(out, Symbol{Name: s.Name.Name, Kind: kind, Container: "parser", Location: goLocation(path, text, fset, s.Name.Pos()), Range: goRange(text, fset, s.Pos(), s.End())})
		case *ast.ValueSpec:
			for _, name := range s.Names {
				out = append(out, Symbol{Name: name.Name, Kind: kind, Container: "parser", Location: goLocation(path, text, fset, name.Pos()), Range: goRange(text, fset, s.Pos(), s.End())})
			}
		}
	}
	return out
}

func goDeclKind(tok token.Token) string {
	switch tok {
	case token.TYPE:
		return "type"
	case token.CONST:
		return "constant"
	case token.VAR:
		return "variable"
	default:
		return ""
	}
}

func goReceiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return goReceiverName(e.X)
	case *ast.IndexExpr:
		return goReceiverName(e.X)
	case *ast.IndexListExpr:
		return goReceiverName(e.X)
	case *ast.SelectorExpr:
		left := goReceiverName(e.X)
		if left == "" {
			return e.Sel.Name
		}
		return left + "." + e.Sel.Name
	default:
		return strings.TrimSpace(exprString(e))
	}
}

func exprString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var b strings.Builder
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(ident.Name)
			return false
		}
		return true
	})
	return b.String()
}

// goSyntaxRangeForNode maps selected Go AST nodes to agent-facing structural
// ranges. The list stays deliberately small to avoid noisy expression-level rows.
func goSyntaxRangeForNode(text string, fset *token.FileSet, n ast.Node) (SyntaxRange, bool) {
	switch v := n.(type) {
	case *ast.FuncDecl:
		kind := "function"
		name := v.Name.Name
		detail := "parser"
		if v.Recv != nil && len(v.Recv.List) > 0 {
			kind = "method"
			detail = goReceiverName(v.Recv.List[0].Type)
		}
		return SyntaxRange{Kind: kind, Name: name, Detail: detail, Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.GenDecl:
		kind := goDeclKind(v.Tok)
		if kind == "" {
			return SyntaxRange{}, false
		}
		return SyntaxRange{Kind: kind, Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.TypeSpec:
		return SyntaxRange{Kind: "type", Name: v.Name.Name, Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.ValueSpec:
		return SyntaxRange{Kind: "value", Name: goValueSpecName(v), Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.BlockStmt:
		return SyntaxRange{Kind: "block", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.IfStmt:
		return SyntaxRange{Kind: "if", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.ForStmt:
		return SyntaxRange{Kind: "for", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.RangeStmt:
		return SyntaxRange{Kind: "range", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.SwitchStmt:
		return SyntaxRange{Kind: "switch", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.TypeSwitchStmt:
		return SyntaxRange{Kind: "type_switch", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	case *ast.SelectStmt:
		return SyntaxRange{Kind: "select", Detail: "parser", Range: goRange(text, fset, v.Pos(), v.End())}, true
	}
	return SyntaxRange{}, false
}

func goValueSpecName(v *ast.ValueSpec) string {
	if v == nil || len(v.Names) == 0 {
		return ""
	}
	names := make([]string, 0, len(v.Names))
	for _, name := range v.Names {
		names = append(names, name.Name)
	}
	return strings.Join(names, ",")
}

func tokenContains(start, end, target token.Pos) bool {
	return start.IsValid() && end.IsValid() && target >= start && target <= end
}

func dedupeSyntaxRanges(in []SyntaxRange) []SyntaxRange {
	seen := map[string]bool{}
	out := make([]SyntaxRange, 0, len(in))
	for _, item := range in {
		key := strings.Join([]string{item.Kind, item.Name, item.Detail, rangeKey(item.Range)}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func sortSyntaxRanges(in []SyntaxRange) []SyntaxRange {
	sort.SliceStable(in, func(i, j int) bool {
		iSpan := rangeLineSpan(in[i].Range)
		jSpan := rangeLineSpan(in[j].Range)
		if iSpan != jSpan {
			return iSpan < jSpan
		}
		if in[i].Range.Start.Line != in[j].Range.Start.Line {
			return in[i].Range.Start.Line > in[j].Range.Start.Line
		}
		return in[i].Range.Start.Character > in[j].Range.Start.Character
	})
	return in
}

func rangeLineSpan(r TextRange) int {
	return (r.End.Line - r.Start.Line) + 1
}

func rangeKey(r TextRange) string {
	parts := []string{
		strconv.Itoa(r.Start.Line), strconv.Itoa(r.Start.Character), strconv.Itoa(r.End.Line), strconv.Itoa(r.End.Character),
	}
	return strings.Join(parts, ":")
}

func goLocation(path, text string, fset *token.FileSet, pos token.Pos) Location {
	p, err := positionForToken(text, fset, pos)
	if err != nil {
		return Location{Path: path}
	}
	return Location{Path: path, Line: p.Line, Character: p.Character}
}

func goRange(text string, fset *token.FileSet, start, end token.Pos) TextRange {
	sp, err := positionForToken(text, fset, start)
	if err != nil {
		sp = Position{}
	}
	ep, err := positionForToken(text, fset, end)
	if err != nil {
		ep = sp
	}
	return TextRange{Start: sp, End: ep}
}

func positionForToken(text string, fset *token.FileSet, pos token.Pos) (Position, error) {
	if !pos.IsValid() {
		return Position{}, nil
	}
	p := fset.Position(pos)
	return PositionForOffset(text, p.Offset)
}
