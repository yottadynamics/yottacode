package codemap

import (
	"path/filepath"
	"regexp"
	"strings"
)

// IsTestFor reports whether candidateRel looks like a test file for
// targetRel, using conservative per-language filename conventions. Matching
// is path-only (no content parsing) and deliberately approximate — false
// negatives are preferred over false positives across unrelated packages, so
// candidates must live in the same directory as the target.
func IsTestFor(candidateRel, targetRel string) bool {
	if candidateRel == "" || targetRel == "" || candidateRel == targetRel {
		return false
	}
	if parentRel(candidateRel) != parentRel(targetRel) {
		return false
	}
	targetBase := filepath.Base(targetRel)
	targetExt := filepath.Ext(targetBase)
	targetStem := strings.TrimSuffix(targetBase, targetExt)
	if targetStem == "" || isTestBaseName(targetBase, targetExt) {
		return false
	}
	candBase := filepath.Base(candidateRel)
	switch targetExt {
	case ".go":
		// Any _test.go file in the same package directory is plausibly
		// relevant, including package-external `_test.go` files that don't
		// share a base name with a single source file.
		return strings.HasSuffix(candBase, "_test.go")
	case ".ts", ".tsx", ".js", ".jsx":
		for _, suffix := range []string{".test", ".spec"} {
			for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
				if candBase == targetStem+suffix+ext {
					return true
				}
			}
		}
		return false
	case ".py":
		return candBase == "test_"+targetBase || candBase == targetStem+"_test.py"
	case ".rs":
		return candBase == targetStem+"_test.rs" || candBase == "test_"+targetBase
	default:
		return false
	}
}

func isTestBaseName(base, ext string) bool {
	stem := strings.TrimSuffix(base, ext)
	switch ext {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".ts", ".tsx", ".js", ".jsx":
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
	case ".py":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(stem, "_test")
	case ".rs":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(stem, "_test")
	default:
		return false
	}
}

// LikelyDocsFor returns indexed markdown nodes that mention targetRel's base
// name or containing directory name. It is intentionally approximate — a
// lightweight relevance signal, not a citation index.
func LikelyDocsFor(idx *CodeIndex, targetRel string, max int) []Node {
	if idx == nil || strings.TrimSpace(targetRel) == "" {
		return nil
	}
	if max <= 0 {
		max = 5
	}
	base := strings.TrimSuffix(filepath.Base(targetRel), filepath.Ext(targetRel))
	dir := filepath.Base(parentRel(targetRel))
	var terms []string
	if len(base) >= 3 {
		terms = append(terms, base)
	}
	if dir != "" && dir != "." && len(dir) >= 3 {
		terms = append(terms, dir)
	}
	if len(terms) == 0 {
		return nil
	}
	patterns := make([]*regexp.Regexp, 0, len(terms))
	for _, t := range terms {
		patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(t)+`\b`))
	}
	var out []Node
	for _, doc := range idx.docCorpus() {
		matched := false
		for _, p := range patterns {
			if p.MatchString(doc.text) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, doc.node)
		if len(out) >= max {
			break
		}
	}
	return out
}
