package codemap

import (
	"context"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// FallbackSource uses yottacode's offline syntax layer when available, then the
// conservative regex scanner. It keeps /map useful without requiring users to
// install language servers first.
type FallbackSource struct{}

func (FallbackSource) Symbols(ctx context.Context, path string) ([]lsp.Symbol, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "fallback", err
	}
	items, err := lsp.FallbackFileSymbols(path)
	source := "fallback"
	if lang, ok := lsp.ResolveFile(path); ok && lsp.SyntaxMode(lang.ID) == "parser" {
		source = "parser"
	}
	return items, source, err
}
