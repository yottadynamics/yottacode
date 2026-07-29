package lsp

import (
	"context"
	"strings"
	"sync"
)

// SyntaxSymbolSource extracts structural symbols without starting a language
// server. Parser-backed implementations give yottacode an offline structure
// layer; languages without one keep using the conservative regex fallback.
type SyntaxSymbolSource interface {
	Symbols(ctx context.Context, path string) ([]Symbol, error)
}

var (
	syntaxSourcesMu sync.RWMutex
	syntaxSources   = map[string]SyntaxSymbolSource{}
)

// RegisterSyntaxSymbolSource installs an offline symbol extractor for a stable
// language ID such as "go" or "typescript". Later registrations replace earlier
// ones so tests and future language packs can override the built-in default.
func RegisterSyntaxSymbolSource(languageID string, source SyntaxSymbolSource) {
	languageID = strings.TrimSpace(strings.ToLower(languageID))
	if languageID == "" {
		return
	}
	syntaxSourcesMu.Lock()
	defer syntaxSourcesMu.Unlock()
	if source == nil {
		delete(syntaxSources, languageID)
		return
	}
	syntaxSources[languageID] = source
}

// SyntaxMode reports the offline structure backend available for a language.
// The value is intentionally compact because lsp_status prints it per language.
func SyntaxMode(languageID string) string {
	languageID = strings.TrimSpace(strings.ToLower(languageID))
	syntaxSourcesMu.RLock()
	_, ok := syntaxSources[languageID]
	syntaxSourcesMu.RUnlock()
	if ok {
		return "parser"
	}
	if _, ok := fallbackSymbolPatterns[languageID]; ok {
		return "regex"
	}
	return "none"
}

func syntaxFileSymbols(ctx context.Context, lang Language, path string) ([]Symbol, bool, error) {
	syntaxSourcesMu.RLock()
	source, ok := syntaxSources[lang.ID]
	syntaxSourcesMu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	items, err := source.Symbols(ctx, path)
	return items, true, err
}
