package codemap

import (
	"context"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// FallbackSource uses yottacode's conservative regex symbol scanner. Results
// are approximate, but they make /map useful without requiring users to install
// language servers first.
type FallbackSource struct{}

func (FallbackSource) Symbols(ctx context.Context, path string) ([]lsp.Symbol, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "fallback", err
	}
	items, err := lsp.FallbackFileSymbols(path)
	return items, "fallback", err
}
