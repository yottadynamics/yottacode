package codemap

import (
	"context"
	"errors"
	"strings"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// LSPSource tries a live language server first and falls back to approximate
// regex symbols when the server is unavailable or lacks document symbols.
type LSPSource struct {
	Manager *lsp.Manager
	Servers map[string][]string
	Root    string
}

func (s LSPSource) Symbols(ctx context.Context, path string) ([]lsp.Symbol, string, error) {
	lang, ok := lsp.ResolveFile(path)
	if !ok {
		return nil, "", nil
	}
	lang = lsp.ApplyOverrides(lang, s.Servers)
	if s.Manager == nil || !lsp.ServerAvailable(lang) {
		items, err := lsp.FallbackFileSymbols(path)
		return items, "fallback", err
	}
	client, err := s.Manager.Acquire(ctx, lang, lsp.WorkspaceRoot(path, lang, s.Root))
	if err != nil {
		items, fbErr := lsp.FallbackFileSymbols(path)
		if fbErr != nil {
			return nil, "fallback", err
		}
		return items, "fallback", nil
	}
	defer client.Close()
	items, err := client.DocumentSymbols(ctx, path)
	if err == nil {
		return items, "lsp", nil
	}
	if errors.Is(err, lsp.ErrUnsupportedCapability) || strings.Contains(err.Error(), "unsupported") {
		items, fbErr := lsp.FallbackFileSymbols(path)
		return items, "fallback", fbErr
	}
	items, fbErr := lsp.FallbackFileSymbols(path)
	if fbErr == nil && len(items) > 0 {
		return items, "fallback", nil
	}
	return nil, "lsp", err
}
