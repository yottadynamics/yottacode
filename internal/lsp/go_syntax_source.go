package lsp

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
