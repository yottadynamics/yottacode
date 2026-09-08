package lsp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// SyntaxKind is a stable, language-independent structural range kind.
type SyntaxKind string

const (
	SyntaxKindFunction    SyntaxKind = "function"
	SyntaxKindMethod      SyntaxKind = "method"
	SyntaxKindType        SyntaxKind = "type"
	SyntaxKindCall        SyntaxKind = "call"
	SyntaxKindImportBlock SyntaxKind = "import_block"
	SyntaxKindField       SyntaxKind = "field"
	SyntaxKindFile        SyntaxKind = "file"
)

// SyntaxSymbolSource extracts structural symbols without starting a language
// server. Implementations may use a grammar parser or a conservative scanner.
type SyntaxSymbolSource interface {
	Symbols(ctx context.Context, path string) ([]Symbol, error)
}

// SyntaxRange is one structural range containing a source position. Byte bounds
// are exact, half-open offsets into the immutable Source snapshot returned with
// the range. Range uses zero-based LSP UTF-16 coordinates.
type SyntaxRange struct {
	Kind      SyntaxKind
	Name      string
	Detail    string
	Range     TextRange
	StartByte int
	EndByte   int
}

// SyntaxRangeResult keeps ranges and diagnostics tied to the bytes that were
// parsed. Warnings are recoverable: all returned ranges remain safe to use.
type SyntaxRangeResult struct {
	Source   []byte
	Ranges   []SyntaxRange
	Warnings []string
}

// SyntaxRangeSource is the optional extension for offline range backends. The
// caller supplies the one immutable file snapshot used for all offsets.
type SyntaxRangeSource interface {
	RangesFromSource(ctx context.Context, path string, src []byte, pos Position) ([]SyntaxRange, []string, error)
}

type syntaxModeSource interface{ SyntaxMode() string }

var (
	syntaxSourcesMu sync.RWMutex
	syntaxSources   = map[string]SyntaxSymbolSource{}
)

// RegisterSyntaxSymbolSource installs an offline symbol extractor for a stable
// language ID. Later registrations replace earlier ones for tests and packs.
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

// SyntaxMode reports whether the offline backend is a grammar parser, a
// structural scanner, a regex fallback, or unavailable.
func SyntaxMode(languageID string) string {
	languageID = strings.TrimSpace(strings.ToLower(languageID))
	syntaxSourcesMu.RLock()
	source, ok := syntaxSources[languageID]
	syntaxSourcesMu.RUnlock()
	if ok {
		if mode, ok := source.(syntaxModeSource); ok {
			return mode.SyntaxMode()
		}
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

// SyntaxFileRanges reads path exactly once and returns ranges, warnings, and
// the snapshot from which every byte offset was derived.
func SyntaxFileRanges(ctx context.Context, lang Language, path string, pos Position) (SyntaxRangeResult, bool, error) {
	syntaxSourcesMu.RLock()
	source, ok := syntaxSources[lang.ID]
	syntaxSourcesMu.RUnlock()
	if !ok {
		return SyntaxRangeResult{}, false, nil
	}
	rangeSource, ok := source.(SyntaxRangeSource)
	if !ok {
		return SyntaxRangeResult{}, false, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return SyntaxRangeResult{}, true, err
	}
	items, warnings, err := rangeSource.RangesFromSource(ctx, path, src, pos)
	if err != nil {
		return SyntaxRangeResult{}, true, err
	}
	for i := range items {
		item := &items[i]
		switch item.Kind {
		case "class", "interface", "enum", "struct", "trait":
			item.Kind = SyntaxKindType
		case "fn":
			item.Kind = SyntaxKindFunction
		}
		// Scanner ranges historically used "parser" as a generic provenance.
		// Keep names/details but report the backend honestly.
		if SyntaxMode(lang.ID) == "scanner" && item.Detail == "parser" {
			item.Detail = "scanner"
		}
		if item.StartByte < 0 || item.EndByte < item.StartByte || item.EndByte > len(src) {
			return SyntaxRangeResult{}, true, fmt.Errorf("invalid syntax byte range %d:%d for %d-byte source", item.StartByte, item.EndByte, len(src))
		}
	}
	return SyntaxRangeResult{Source: src, Ranges: sortSyntaxRanges(dedupeSyntaxRanges(items)), Warnings: warnings}, true, nil
}

func syntaxRangeFromBytes(text string, kind SyntaxKind, name, detail string, start, end int) (SyntaxRange, bool) {
	if start < 0 || end < start || end > len(text) {
		return SyntaxRange{}, false
	}
	sp, err1 := PositionForOffset(text, start)
	ep, err2 := PositionForOffset(text, end)
	if err1 != nil || err2 != nil {
		return SyntaxRange{}, false
	}
	return SyntaxRange{Kind: kind, Name: name, Detail: detail, StartByte: start, EndByte: end, Range: TextRange{Start: sp, End: ep}}, true
}
