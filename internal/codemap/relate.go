package codemap

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Reason explains why a node was suggested as relevant context around a
// change. Multiple reasons can apply to the same node; callers combine them.
type Reason string

const (
	ReasonChanged    Reason = "changed"
	ReasonImports    Reason = "imports target"
	ReasonImportedBy Reason = "imported by target"
	ReasonTestFor    Reason = "test for target"
	ReasonDocsFor    Reason = "docs for target"
)

// reasonScore weights each reason for SuggestedContext ranking. Changed is
// deliberately far above the others (whose scores can sum when a node
// matches more than one) so an edited file always sorts first regardless of
// how many other reasons a neighbor accumulates.
func reasonScore(r Reason) int {
	switch r {
	case ReasonChanged:
		return 1000
	case ReasonTestFor:
		return 70
	case ReasonImports, ReasonImportedBy:
		return 50
	case ReasonDocsFor:
		return 40
	default:
		return 0
	}
}

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
	for _, n := range idx.Nodes() {
		if n.Kind != NodeFile || n.Language != "markdown" {
			continue
		}
		content, err := os.ReadFile(n.Path)
		if err != nil {
			continue
		}
		text := string(content)
		matched := false
		for _, p := range patterns {
			if p.MatchString(text) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, n)
		if len(out) >= max {
			break
		}
	}
	return out
}

// SuggestedItem is one ranked candidate for prompt context around a change.
type SuggestedItem struct {
	Node    Node
	Reasons []Reason
	Score   int
}

// SuggestedContext ranks files worth attaching before asking the agent about
// a set of changed files: the changed files themselves, their direct import
// neighbors, their likely tests, and docs that mention them.
func SuggestedContext(idx *CodeIndex, changedRelPaths []string, max int) []SuggestedItem {
	if idx == nil || len(changedRelPaths) == 0 {
		return nil
	}
	if max <= 0 {
		max = 8
	}
	items := map[NodeID]*SuggestedItem{}
	addReason := func(n Node, r Reason) {
		if n.ID == "" {
			return
		}
		it, ok := items[n.ID]
		if !ok {
			it = &SuggestedItem{Node: n}
			items[n.ID] = it
		}
		if slices.Contains(it.Reasons, r) {
			return
		}
		it.Reasons = append(it.Reasons, r)
		it.Score += reasonScore(r)
	}

	for _, rel := range changedRelPaths {
		targetID := idx.firstFileMatch(rel)
		if targetID == "" {
			continue
		}
		target := idx.nodes[targetID]
		addReason(target, ReasonChanged)
		for _, dep := range idx.Dependencies(target.RelPath, 8) {
			addReason(dep, ReasonImports)
		}
		for _, dep := range idx.Dependents(target.RelPath, 8) {
			addReason(dep, ReasonImportedBy)
		}
		for _, sibling := range idx.filesInDir(parentRel(target.RelPath)) {
			if IsTestFor(sibling.RelPath, target.RelPath) {
				addReason(sibling, ReasonTestFor)
			}
		}
		for _, doc := range LikelyDocsFor(idx, target.RelPath, 3) {
			addReason(doc, ReasonDocsFor)
		}
	}

	out := make([]SuggestedItem, 0, len(items))
	for _, it := range items {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Node.RelPath < out[j].Node.RelPath
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}
