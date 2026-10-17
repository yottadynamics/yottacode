package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

const (
	// codeImpactQueryCap bounds how many files each graph query collects before
	// the output budget trims what is rendered. A section that hits it is
	// reported as "N+" rather than as an exact count.
	codeImpactQueryCap = 500

	codeImpactDefaultDepth  = 3
	codeImpactDefaultBudget = 1500 // approximate tokens
	codeImpactMaxBudget     = 4000
	approxBytesPerToken     = 4

	// codeImpactLSPTimeout bounds the optional live LSP supplement so a slow or
	// wedged language server cannot stall a call whose static answer is ready.
	codeImpactLSPTimeout = 3 * time.Second

	// codeImpactMaxCallSymbols bounds how many of the target's exported symbols
	// are sent to the language server, each a separate round trip.
	codeImpactMaxCallSymbols = 25
)

// CodeImpactTool answers file-level blast-radius questions over the static
// import graph: who depends on a file, what it depends on, which tests and docs
// are likely affected, and which import cycles it sits in. It is the single
// Code Map agent tool; symbol lookup and structure belong to the lsp_* tools and
// list_project_structure.
//
// Every section is labeled with how it was derived (import edge, name
// heuristic, or live LSP) and the header states when the index is partial.
// LSP is optional: with include_calls it best-effort supplements the report
// with call-hierarchy callers/callees, a symbol-scoped, slower signal that
// stays out of the cached static graph on purpose.
type CodeImpactTool struct {
	Provider      codemap.Provider
	LSP           lspToolBase
	DenyReadPaths []string
}

func (t *CodeImpactTool) Name() string { return "code_impact" }
func (t *CodeImpactTool) Description() string {
	return "Blast radius of changing a file, from the repository import graph (works without a language server). " +
		"mode=impact (default) lists direct and transitive dependents, direct dependencies, the test directories to run, likely docs/config, and import cycles; " +
		"mode=tests returns just the tests to run; mode=dependents / dependencies / cycles return just that list. " +
		"Tests default to scope=package: every directory that imports the target's directory, transitively, that holds tests (conservative; use this to choose what to run). scope=file is narrower and misses tests that reach the change through other files of the package. " +
		"Each section is labeled [import] (resolved import edges) or [heuristic] (name-matched guesses, not proof). " +
		"The header states when the index is partial, in which case absence of a dependent proves nothing. " +
		"Use before editing a shared file to find what to re-test; use lsp_* tools for symbol-level references."
}
func (t *CodeImpactTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":          map[string]any{"type": "string", "description": "File path or path fragment to analyze"},
		"mode":          map[string]any{"type": "string", "enum": []string{"impact", "tests", "dependents", "dependencies", "cycles"}, "description": "What to return (default impact)"},
		"scope":         map[string]any{"type": "string", "enum": []string{"package", "file"}, "description": "How tests are selected: package (default, conservative directory-level closure) or file (narrow; only test files among the target's file-level dependents)"},
		"depth":         map[string]any{"type": "integer", "description": fmt.Sprintf("How many import hops of transitive dependents to follow; -1 means all (default %d)", codeImpactDefaultDepth)},
		"budget":        map[string]any{"type": "integer", "description": fmt.Sprintf("Approximate output size in tokens (default %d, max %d); items beyond it are omitted with a count", codeImpactDefaultBudget, codeImpactMaxBudget)},
		"include_calls": map[string]any{"type": "boolean", "description": "Best-effort supplement with live LSP call-hierarchy callers/callees for the target file's exported symbols when a language server is available (default false; slower than the static graph query)"},
	}, "required": []string{"path"}}
}
func (t *CodeImpactTool) RequiresApproval(string) bool { return false }
func (t *CodeImpactTool) ParallelSafe(string) bool     { return true }
func (t *CodeImpactTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if m := strings.TrimSpace(a.Mode); m != "" && m != "impact" {
		return fmt.Sprintf("code_impact(%s, %s)", a.Path, m)
	}
	return fmt.Sprintf("code_impact(%s)", a.Path)
}

type codeImpactArgs struct {
	Path         string `json:"path"`
	Mode         string `json:"mode"`
	Scope        string `json:"scope"`
	Depth        *int   `json:"depth"`
	Budget       int    `json:"budget"`
	IncludeCalls bool   `json:"include_calls"`
}

func (t *CodeImpactTool) Execute(ctx context.Context, argsJSON string) (out string, err error) {
	// An experimental, read-only analysis tool must never be able to crash the
	// session over a bug of its own; report it as a tool error instead.
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("code_impact: internal error: %v", r)
		}
	}()
	var a codeImpactArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("code_impact: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("code_impact: path is required")
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	switch mode {
	case "":
		mode = "impact"
	case "impact", "tests", "dependents", "dependencies", "cycles":
	default:
		return "", fmt.Errorf("code_impact: unknown mode %q (want impact, tests, dependents, dependencies, or cycles)", a.Mode)
	}
	scope := strings.ToLower(strings.TrimSpace(a.Scope))
	switch scope {
	case "":
		scope = "package"
	case "package", "file":
	default:
		return "", fmt.Errorf("code_impact: unknown scope %q (want package or file)", a.Scope)
	}
	depth := codeImpactDefaultDepth
	if a.Depth != nil && *a.Depth != 0 {
		depth = *a.Depth
	}
	if depth < 0 {
		depth = codemap.MaxDepthAll
	}

	idx, err := codeMapIndex(ctx, t.Provider)
	if err != nil {
		return "", fmt.Errorf("code_impact: %w", err)
	}
	all := idx.FindFiles(a.Path)
	// Denied files are invisible: they neither count toward ambiguity nor appear
	// in the candidate list, so the tool cannot be used to learn their names.
	matches := t.allowed(idx, all)
	switch {
	case len(all) == 0:
		return fmt.Sprintf("code_impact %s %s\nindex: %s\nno indexed file matches %q (the index only covers supported source and markdown files)\n", mode, a.Path, idx.Info(), a.Path), nil
	case len(matches) == 0:
		return "", fmt.Errorf("code_impact: %s is on the read deny list", a.Path)
	case len(matches) > 1:
		return ambiguousPathResult(mode, a.Path, idx.Info(), matches), nil
	}
	target := matches[0]

	report := codemap.Report{Mode: mode, Target: target.RelPath, Info: idx.Info(), Sections: t.sections(idx, target, mode, scope, depth)}
	if a.IncludeCalls {
		lspCtx, cancel := context.WithTimeout(ctx, codeImpactLSPTimeout)
		defer cancel()
		report.Sections = append(report.Sections, codemap.ReportSection{
			Title: "calls", Provenance: codemap.ProvLSP, Raw: t.callGraphSupplement(lspCtx, idx, target, codeImpactQueryCap),
		})
	}
	return report.Render(codeImpactBudgetBytes(a.Budget)), nil
}

// sections runs one mode's graph queries and labels each result by provenance.
func (t *CodeImpactTool) sections(idx *codemap.CodeIndex, target codemap.Node, mode, scope string, depth int) []codemap.ReportSection {
	var out []codemap.ReportSection
	files := func(title string, prov codemap.Provenance, nodes []codemap.Node) {
		out = append(out, codemap.ReportSection{
			Title: title, Provenance: prov, Nodes: t.allowed(idx, nodes), AtLeast: len(nodes) >= codeImpactQueryCap,
		})
	}
	cycles := func(cs [][]codemap.Node) {
		out = append(out, codemap.ReportSection{
			Title: "cycles", Provenance: codemap.ProvImport, Cycles: t.allowedCycles(idx, cs), AtLeast: len(cs) >= codeImpactQueryCap,
		})
	}
	tests := func(res codemap.ImpactResult) {
		keep := func(n codemap.Node) bool { return !t.denied(idx, n) }
		title := "test directories to run (package level)"
		var dirs []codemap.TestDir
		if scope == "file" {
			title = "test directories to run (file level, narrow)"
			dirs = idx.TestDirsFromImpact(target, res, keep)
		} else {
			dirs = idx.TestDirsToRun(target, keep, codeImpactQueryCap)
		}
		lines := make([]string, 0, len(dirs))
		for _, d := range dirs {
			lines = append(lines, fmt.Sprintf("%s/ (%d test files)", strings.TrimSuffix(d.Dir, "/"), d.Files))
		}
		out = append(out, codemap.ReportSection{
			Title: title, Provenance: codemap.ProvImport, Raw: lines, AtLeast: len(dirs) >= codeImpactQueryCap,
		})
	}
	switch mode {
	case "tests":
		var res codemap.ImpactResult
		if scope == "file" {
			res = idx.Impact(target.RelPath, codemap.MaxDepthAll, codeImpactQueryCap)
		}
		tests(res)
	case "dependents":
		files("dependents", codemap.ProvImport, idx.Dependents(target.RelPath, codeImpactQueryCap))
	case "dependencies":
		files("dependencies", codemap.ProvImport, idx.Dependencies(target.RelPath, codeImpactQueryCap))
	case "cycles":
		cycles(idx.Cycles(target.RelPath, codeImpactQueryCap))
	default:
		res := idx.Impact(target.RelPath, depth, codeImpactQueryCap)
		files("direct dependents", codemap.ProvImport, res.DirectDependents)
		files(fmt.Sprintf("indirect dependents (depth %s)", depthText(depth)), codemap.ProvImport, withoutNodes(res.TransitiveDependents, res.DirectDependents))
		files("direct dependencies", codemap.ProvImport, res.DirectDependencies)
		tests(res)
		files("likely docs/config", codemap.ProvHeuristic, res.LikelyDocs)
		cycles(res.Cycles)
	}
	return out
}

func depthText(depth int) string {
	if depth == codemap.MaxDepthAll {
		return "all"
	}
	return fmt.Sprint(depth)
}

func codeImpactBudgetBytes(tokens int) int {
	if tokens <= 0 {
		tokens = codeImpactDefaultBudget
	}
	if tokens > codeImpactMaxBudget {
		tokens = codeImpactMaxBudget
	}
	return tokens * approxBytesPerToken
}

func (t *CodeImpactTool) denied(idx *codemap.CodeIndex, n codemap.Node) bool {
	if len(t.DenyReadPaths) == 0 {
		return false
	}
	return ValidateReadPath(filepath.Join(idx.RootPath(), filepath.FromSlash(n.RelPath)), t.DenyReadPaths) != nil
}

// allowed drops files on the read deny list so their names are not leaked
// through the graph.
func (t *CodeImpactTool) allowed(idx *codemap.CodeIndex, nodes []codemap.Node) []codemap.Node {
	if len(t.DenyReadPaths) == 0 {
		return nodes
	}
	out := make([]codemap.Node, 0, len(nodes))
	for _, n := range nodes {
		if !t.denied(idx, n) {
			out = append(out, n)
		}
	}
	return out
}

// allowedCycles drops any cycle that passes through a denied file.
func (t *CodeImpactTool) allowedCycles(idx *codemap.CodeIndex, cycles [][]codemap.Node) [][]codemap.Node {
	if len(t.DenyReadPaths) == 0 {
		return cycles
	}
	out := make([][]codemap.Node, 0, len(cycles))
	for _, c := range cycles {
		if len(t.allowed(idx, c)) == len(c) {
			out = append(out, c)
		}
	}
	return out
}

// callGraphSupplement best-effort collects live LSP call-hierarchy callers and
// callees for the target file's exported symbols as report lines. It never
// fails the tool call — an unavailable or unsupported language server is
// reported in the lines themselves, mirroring lsp_impact's
// writeImpactUnavailable style.
func (t *CodeImpactTool) callGraphSupplement(ctx context.Context, idx *codemap.CodeIndex, target codemap.Node, limit int) []string {
	unavailable := func(msg string) []string {
		return []string{"callers: " + msg, "callees: " + msg}
	}
	absPath := filepath.Join(idx.RootPath(), filepath.FromSlash(target.RelPath))
	lang, ok := lspci.ResolveFile(absPath)
	if !ok {
		return unavailable(strings.TrimSpace(unsupportedFileResult(absPath)))
	}
	if t.LSP.Disabled[lang.ID] {
		return unavailable(fmt.Sprintf("unavailable: LSP language %s is disabled by config", lang.ID))
	}
	lang = lspci.ApplyOverrides(lang, t.LSP.Servers)
	if !lspci.ServerAvailable(lang) && t.LSP.NewClient == nil {
		return unavailable(strings.TrimSpace(unavailableServerResult(lang)))
	}
	client, err := t.LSP.openClient(ctx, lang, lspci.WorkspaceRoot(absPath, lang, idx.RootPath()))
	if err != nil {
		return unavailable(strings.TrimSpace(missingServerResult(lang, err)))
	}
	defer client.Close()

	var callers, callees []lspci.CallHierarchyItem
	queried, total := 0, 0
	source := fileLines(absPath)
	for _, sym := range idx.SymbolsForFile(target.RelPath) {
		if !sym.Symbol.Exported {
			continue
		}
		total++
		if queried >= codeImpactMaxCallSymbols {
			continue
		}
		queried++
		items, err := client.CallHierarchy(ctx, absPath, symbolNamePosition(source, sym))
		if err != nil {
			continue
		}
		for _, item := range items {
			if len(t.DenyReadPaths) > 0 && ValidateReadPath(item.Location.Path, t.DenyReadPaths) != nil {
				continue
			}
			if item.Direction == "outgoing" {
				callees = append(callees, item)
			} else {
				callers = append(callers, item)
			}
		}
	}
	var lines []string
	add := func(label string, items []lspci.CallHierarchyItem) {
		for i, item := range items {
			if i >= limit {
				break
			}
			loc := item.Location
			if rel, err := filepath.Rel(idx.RootPath(), loc.Path); err == nil && !strings.HasPrefix(rel, "..") {
				loc.Path = filepath.ToSlash(rel)
			}
			lines = append(lines, fmt.Sprintf("%s %s\t%s\t%s", label, displayLocation(loc), item.Kind, item.Name))
		}
	}
	add("caller", callers)
	add("callee", callees)
	if len(lines) == 0 {
		lines = []string{"(no callers or callees reported)"}
	}
	if total > queried {
		lines = append(lines, fmt.Sprintf("(call hierarchy asked for the first %d of %d exported symbols)", queried, total))
	}
	return lines
}

func codeMapIndex(ctx context.Context, provider codemap.Provider) (*codemap.CodeIndex, error) {
	if provider == nil {
		return nil, fmt.Errorf("code map is not enabled; start with --experimental code_map")
	}
	idx, err := provider.Index(ctx)
	if err != nil {
		return nil, err
	}
	if idx == nil {
		return nil, fmt.Errorf("code map is unavailable")
	}
	return idx, nil
}

// ambiguousPathResult answers a fragment that names several files with the
// candidates instead of analyzing an arbitrary one of them.
func ambiguousPathResult(mode, query string, info codemap.IndexInfo, matches []codemap.Node) string {
	const shown = 10
	var b strings.Builder
	fmt.Fprintf(&b, "code_impact %s %s\nindex: %s\n", mode, query, info)
	fmt.Fprintf(&b, "%q matches %d files; pass a more specific path:\n", query, len(matches))
	for i, m := range matches {
		if i == shown {
			fmt.Fprintf(&b, "  …and %d more\n", len(matches)-shown)
			break
		}
		fmt.Fprintf(&b, "  %s\n", m.RelPath)
	}
	return b.String()
}

// withoutNodes returns nodes minus any that also appear in drop. The index's
// transitive walk includes the direct dependents; the report lists them once.
func withoutNodes(nodes, drop []codemap.Node) []codemap.Node {
	skip := make(map[codemap.NodeID]bool, len(drop))
	for _, n := range drop {
		skip[n.ID] = true
	}
	out := make([]codemap.Node, 0, len(nodes))
	for _, n := range nodes {
		if !skip[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

func fileLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// symbolNamePosition returns where a symbol's name starts, which is where call
// hierarchy must be asked. The index records a symbol's range from the start of
// its declaration (the `func` or `def` keyword), and a language server answers
// nothing for a position that is not on the identifier. The name is found on the
// declaration's first line; among several occurrences (a method named like its
// receiver type) the one followed by a call or type-parameter list wins. When the
// name cannot be located the declaration start is returned unchanged. The column
// is counted in UTF-16 code units, as LSP requires.
func symbolNamePosition(lines []string, sym codemap.Node) lspci.Position {
	start := lspci.Position{Line: sym.Symbol.Range.Start.Line, Character: sym.Symbol.Range.Start.Character}
	if start.Line < 0 || start.Line >= len(lines) || sym.Name == "" {
		return start
	}
	line := lines[start.Line]
	first, preferred := -1, -1
	for from := 0; from < len(line); {
		i := strings.Index(line[from:], sym.Name)
		if i < 0 {
			break
		}
		i += from
		end := i + len(sym.Name)
		from = end
		if i > 0 && isIdentByte(line[i-1]) || end < len(line) && isIdentByte(line[end]) {
			continue
		}
		if first < 0 {
			first = i
		}
		if end < len(line) && (line[end] == '(' || line[end] == '[') {
			preferred = i
			break
		}
	}
	col := preferred
	if col < 0 {
		col = first
	}
	if col < 0 {
		return start
	}
	return lspci.Position{Line: start.Line, Character: len(utf16.Encode([]rune(line[:col])))}
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}
