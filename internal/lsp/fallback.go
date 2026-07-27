package lsp

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var fallbackSymbolPatterns = map[string][]struct {
	kind string
	re   *regexp.Regexp
}{
	"go": {
		{"function", regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{"type", regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		{"variable", regexp.MustCompile(`^\s*(?:var|const)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
	},
	"typescript": {
		{"function", regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)},
		{"class", regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)},
		{"interface", regexp.MustCompile(`^\s*(?:export\s+)?interface\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)},
		{"variable", regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)},
	},
	"python": {
		{"function", regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{"class", regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
	},
	"rust": {
		{"function", regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{"struct", regexp.MustCompile(`^\s*(?:pub\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		{"enum", regexp.MustCompile(`^\s*(?:pub\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		{"trait", regexp.MustCompile(`^\s*(?:pub\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
	},
}

// FallbackSymbols scans source files with conservative regexes when a real
// language server is unavailable. Results are intentionally approximate; the
// output marks Container as "fallback" so callers can surface that reduced
// precision to the model/user.
func FallbackSymbols(ctx context.Context, root, query string, maxFiles int) ([]Symbol, error) {
	if maxFiles <= 0 {
		maxFiles = 2000
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var out []Symbol
	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > maxFiles {
			return filepath.SkipAll
		}
		lang, ok := ResolveFile(path)
		if !ok {
			return nil
		}
		patterns := fallbackSymbolPatterns[lang.ID]
		if len(patterns) == 0 {
			return nil
		}
		items, err := scanFallbackFile(path, query, patterns)
		if err != nil {
			return nil
		}
		out = append(out, items...)
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return nil, err
	}
	return out, nil
}

// FallbackFileSymbols scans one supported source file with the same conservative
// regexes used by FallbackSymbols. It is intended for outline-style callers that
// already walked the workspace and need deterministic per-file results.
func FallbackFileSymbols(path string) ([]Symbol, error) {
	lang, ok := ResolveFile(path)
	if !ok {
		return nil, nil
	}
	patterns := fallbackSymbolPatterns[lang.ID]
	if len(patterns) == 0 {
		return nil, nil
	}
	return scanFallbackFile(path, "", patterns)
}

func scanFallbackFile(path, query string, patterns []struct {
	kind string
	re   *regexp.Regexp
}) ([]Symbol, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Symbol
	s := bufio.NewScanner(f)
	line := 0
	for s.Scan() {
		text := s.Text()
		for _, p := range patterns {
			m := p.re.FindStringSubmatch(text)
			if len(m) < 2 {
				continue
			}
			name := m[1]
			if query != "" && !strings.Contains(strings.ToLower(name), query) {
				continue
			}
			col := strings.Index(text, name)
			if col < 0 {
				col = 0
			}
			out = append(out, Symbol{Name: name, Kind: p.kind, Container: "fallback", Location: Location{Path: path, Line: line, Character: col}, Range: TextRange{Start: Position{Line: line, Character: col}, End: Position{Line: line, Character: col + len(name)}}})
		}
		line++
	}
	return out, s.Err()
}
