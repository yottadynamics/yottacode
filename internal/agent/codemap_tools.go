package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

const defaultCodeMapMax = 120

// codeMapExportMax is the effective "unbounded" cap used when a code-map
// tool writes its output to a file instead of returning it inline — large
// enough that no real repo's diagram or projection hits it, so a to_file
// export is genuinely complete rather than truncated.
const codeMapExportMax = 1_000_000

// writeCodeMapExport validates and writes a to_file export the same way
// WriteFileTool does (see fs_tools.go): resolve against cwd, run it through
// the normal write-path validator, create parent dirs, then write. label
// names the tool in error messages and the success confirmation.
func writeCodeMapExport(cwd *CwdRef, writeOpts WritePathOptions, toFile, content, label string) (string, error) {
	p := resolvePath(cwd.Get(), toFile)
	if err := ValidateWritePath(p, writeOpts); err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("%s: mkdir: %w", label, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Sprintf("%s written to %s (%d bytes)\n", label, p, len(content)), nil
}

func codeMapToFileArg(argsJSON string) string {
	var a struct {
		ToFile string `json:"to_file"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return strings.TrimSpace(a.ToFile)
}

// CodeMapTool renders a bounded repository outline from the shared code index.
type CodeMapTool struct{ Provider codemap.Provider }

func (t *CodeMapTool) Name() string { return "code_map" }
func (t *CodeMapTool) Description() string {
	return "Return a bounded repository structure map (directories, files, symbols) from yottacode's code index. Use this for cheap codebase orientation before reading files."
}
func (t *CodeMapTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query":       map[string]any{"type": "string", "description": "Optional filter for paths, symbols, kinds, or containers"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned nodes (default 120, max 500)"},
	}}
}
func (t *CodeMapTool) RequiresApproval(string) bool { return false }
func (t *CodeMapTool) ParallelSafe(string) bool     { return true }
func (t *CodeMapTool) PreviewCall(argsJSON string) string {
	var a struct{ Query string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Query) != "" {
		return fmt.Sprintf("code_map(%q)", a.Query)
	}
	return "code_map()"
}
func (t *CodeMapTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, query, max, err := codeMapArgs(ctx, t.Provider, argsJSON)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(query) != "" {
		return codemap.FormatNodes(idx.Filter(query, max), max), nil
	}
	return codemap.FormatTree(idx, max), nil
}

// CodeSymbolsTool returns symbols for one file or query from the code index.
type CodeSymbolsTool struct{ Provider codemap.Provider }

func (t *CodeSymbolsTool) Name() string { return "code_symbols" }
func (t *CodeSymbolsTool) Description() string {
	return "Return indexed symbols for a source file or search query. Read-only and capped; use before opening files when symbol names are enough."
}
func (t *CodeSymbolsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "Optional source file path"},
		"query":       map[string]any{"type": "string", "description": "Optional symbol/path search query"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned symbols (default 120, max 500)"},
	}}
}
func (t *CodeSymbolsTool) RequiresApproval(string) bool { return false }
func (t *CodeSymbolsTool) ParallelSafe(string) bool     { return true }
func (t *CodeSymbolsTool) PreviewCall(argsJSON string) string {
	var a struct{ Path, Query string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Path != "" {
		return fmt.Sprintf("code_symbols(%s)", a.Path)
	}
	return fmt.Sprintf("code_symbols(%q)", a.Query)
}
func (t *CodeSymbolsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("code_symbols: invalid args: %w", err)
	}
	idx, err := codeMapIndex(ctx, t.Provider)
	if err != nil {
		return "", fmt.Errorf("code_symbols: %w", err)
	}
	max := normalizedCodeMapMax(a.MaxResults)
	if strings.TrimSpace(a.Path) != "" {
		return codemap.FormatNodes(idx.SymbolsForFile(a.Path), max), nil
	}
	matches := idx.Filter(a.Query, max)
	syms := make([]codemap.Node, 0, len(matches))
	for _, n := range matches {
		if n.Kind == codemap.NodeSymbol {
			syms = append(syms, n)
		}
	}
	return codemap.FormatNodes(syms, max), nil
}

// CodeStructureProjectionTool returns a compact context projection for
// agents. Set to_file to write the full, unbounded projection to disk
// instead — a bounded-for-context-window / unbounded-on-disk export surface.
type CodeStructureProjectionTool struct {
	Provider  codemap.Provider
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *CodeStructureProjectionTool) Name() string { return "code_structure_projection" }
func (t *CodeStructureProjectionTool) Description() string {
	return "Generate a compact, token-efficient structure projection from the code index: package/file tree, key symbols, and counts. Set to_file to write the full projection to disk instead of returning it inline."
}
func (t *CodeStructureProjectionTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"max_results": map[string]any{"type": "integer", "description": "Cap on files included (default 120, max 500)"},
		"to_file":     map[string]any{"type": "string", "description": "Optional path to write the full, untruncated projection instead of returning it inline"},
	}}
}
func (t *CodeStructureProjectionTool) RequiresApproval(argsJSON string) bool {
	return codeMapToFileArg(argsJSON) != ""
}
func (t *CodeStructureProjectionTool) ParallelSafe(argsJSON string) bool {
	return codeMapToFileArg(argsJSON) == ""
}
func (t *CodeStructureProjectionTool) PreviewCall(argsJSON string) string {
	if toFile := codeMapToFileArg(argsJSON); toFile != "" {
		return fmt.Sprintf("code_structure_projection(to_file=%s)", toFile)
	}
	return "code_structure_projection()"
}
func (t *CodeStructureProjectionTool) PathsToSnapshot(cwd, argsJSON string) []string {
	if toFile := codeMapToFileArg(argsJSON); toFile != "" {
		return []string{resolvePath(cwd, toFile)}
	}
	return nil
}
func (t *CodeStructureProjectionTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, _, max, err := codeMapArgs(ctx, t.Provider, argsJSON)
	if err != nil {
		return "", err
	}
	if toFile := codeMapToFileArg(argsJSON); toFile != "" {
		return writeCodeMapExport(t.Cwd, t.WriteOpts, toFile, codemap.Projection(idx, codeMapExportMax), "code_structure_projection")
	}
	return codemap.Projection(idx, max), nil
}

type CodeDependenciesTool struct{ Provider codemap.Provider }

func (t *CodeDependenciesTool) Name() string { return "code_dependencies" }
func (t *CodeDependenciesTool) Description() string {
	return "Return outgoing import dependencies for an indexed file/path query from the experimental code map."
}
func (t *CodeDependenciesTool) Schema() map[string]any       { return codeDependencySchema() }
func (t *CodeDependenciesTool) RequiresApproval(string) bool { return false }
func (t *CodeDependenciesTool) ParallelSafe(string) bool     { return true }
func (t *CodeDependenciesTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	return fmt.Sprintf("code_dependencies(%s)", path)
}
func (t *CodeDependenciesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, path, max, err := executeCodeDependencyArgs(ctx, t.Provider, argsJSON, "code_dependencies")
	if err != nil {
		return "", err
	}
	return codemap.FormatDependencies("dependencies\t"+path, idx.Dependencies(path, max), max), nil
}

type CodeDependentsTool struct{ Provider codemap.Provider }

func (t *CodeDependentsTool) Name() string { return "code_dependents" }
func (t *CodeDependentsTool) Description() string {
	return "Return incoming import dependents for an indexed file/path query from the experimental code map."
}
func (t *CodeDependentsTool) Schema() map[string]any       { return codeDependencySchema() }
func (t *CodeDependentsTool) RequiresApproval(string) bool { return false }
func (t *CodeDependentsTool) ParallelSafe(string) bool     { return true }
func (t *CodeDependentsTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	return fmt.Sprintf("code_dependents(%s)", path)
}
func (t *CodeDependentsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, path, max, err := executeCodeDependencyArgs(ctx, t.Provider, argsJSON, "code_dependents")
	if err != nil {
		return "", err
	}
	return codemap.FormatDependencies("dependents\t"+path, idx.Dependents(path, max), max), nil
}

// CodeImpactTool answers file-level blast-radius queries over the static
// import graph. LSP is optional: when include_calls is set and a language
// server is available for the target file, it best-effort supplements the
// report with live call-hierarchy callers/callees — a symbol-position-scoped,
// slower signal that stays out of the cached static graph on purpose.
type CodeImpactTool struct {
	Provider codemap.Provider
	LSP      lspToolBase
}

func (t *CodeImpactTool) Name() string { return "code_impact" }
func (t *CodeImpactTool) Description() string {
	return "Return a conservative blast-radius summary for an indexed file/path query: direct dependencies, direct dependents, transitive dependents, likely tests, likely docs/config, and import cycles. Optionally supplements with live LSP callers/callees via include_calls."
}
func (t *CodeImpactTool) Schema() map[string]any       { return codeImpactSchema() }
func (t *CodeImpactTool) RequiresApproval(string) bool { return false }
func (t *CodeImpactTool) ParallelSafe(string) bool     { return true }
func (t *CodeImpactTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	return fmt.Sprintf("code_impact(%s)", path)
}
func (t *CodeImpactTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, path, max, depth, err := executeCodeImpactArgs(ctx, t.Provider, argsJSON, "code_impact")
	if err != nil {
		return "", err
	}
	result := idx.Impact(path, depth, max)
	var out string
	if codeImpactFormat(argsJSON) == "summary" {
		out = codemap.FormatImpactSummary(result, 3)
	} else {
		out = codemap.FormatImpact(result, max)
	}
	if codeImpactIncludeCalls(argsJSON) && result.Target.ID != "" {
		out += t.callGraphSupplement(ctx, idx, result.Target, max)
	}
	return out, nil
}

func codeImpactFormat(argsJSON string) string {
	var a struct {
		Format string `json:"format"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return strings.ToLower(strings.TrimSpace(a.Format))
}

func codeImpactIncludeCalls(argsJSON string) bool {
	var a struct {
		IncludeCalls bool `json:"include_calls"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.IncludeCalls
}

// callGraphSupplement best-effort appends live LSP call-hierarchy callers and
// callees for the target file's exported symbols. It never fails the tool
// call — an unavailable or unsupported language server just gets reported in
// the section body, mirroring lsp_impact's writeImpactUnavailable style.
func (t *CodeImpactTool) callGraphSupplement(ctx context.Context, idx *codemap.CodeIndex, target codemap.Node, limit int) string {
	var b strings.Builder
	b.WriteString("callers (LSP)\n")
	calleesHeader := "callees (LSP)\n"
	if target.ID == "" || idx == nil {
		b.WriteString("  (none)\n")
		return b.String() + calleesHeader + "  (none)\n"
	}
	absPath := filepath.Join(idx.RootPath(), filepath.FromSlash(target.RelPath))
	lang, ok := lspci.ResolveFile(absPath)
	if !ok {
		msg := "  " + unsupportedFileResult(absPath)
		return b.String() + msg + calleesHeader + msg
	}
	if t.LSP.Disabled[lang.ID] {
		msg := fmt.Sprintf("  unavailable: LSP language %s is disabled by config\n", lang.ID)
		return b.String() + msg + calleesHeader + msg
	}
	lang = lspci.ApplyOverrides(lang, t.LSP.Servers)
	if !lspci.ServerAvailable(lang) && t.LSP.NewClient == nil {
		msg := "  " + unavailableServerResult(lang)
		return b.String() + msg + calleesHeader + msg
	}
	client, err := t.LSP.openClient(ctx, lang, lspci.WorkspaceRoot(absPath, lang, idx.RootPath()))
	if err != nil {
		msg := "  " + missingServerResult(lang, err)
		return b.String() + msg + calleesHeader + msg
	}
	defer client.Close()

	var callers, callees []lspci.CallHierarchyItem
	for _, sym := range idx.SymbolsForFile(target.RelPath) {
		if !sym.Symbol.Exported {
			continue
		}
		pos := lspci.Position{Line: sym.Symbol.Range.Start.Line, Character: sym.Symbol.Range.Start.Character}
		items, err := client.CallHierarchy(ctx, absPath, pos)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.Direction == "outgoing" {
				callees = append(callees, item)
			} else {
				callers = append(callers, item)
			}
		}
	}
	writeCallItems(&b, callers, limit)
	b.WriteString(calleesHeader)
	writeCallItems(&b, callees, limit)
	return b.String()
}

func writeCallItems(b *strings.Builder, items []lspci.CallHierarchyItem, limit int) {
	if len(items) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for i, item := range items {
		if i >= limit {
			fmt.Fprintf(b, "  …[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintf(b, "  %s\t%s\t%s\n", displayLocation(item.Location), item.Kind, item.Name)
	}
}

type CodeCyclesTool struct{ Provider codemap.Provider }

func (t *CodeCyclesTool) Name() string { return "code_cycles" }
func (t *CodeCyclesTool) Description() string {
	return "Return import cycles from the experimental code map, optionally narrowed to cycles involving one file/path query."
}
func (t *CodeCyclesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "Optional file path or path fragment to narrow cycles"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned cycles (default 120, max 500)"},
	}}
}
func (t *CodeCyclesTool) RequiresApproval(string) bool { return false }
func (t *CodeCyclesTool) ParallelSafe(string) bool     { return true }
func (t *CodeCyclesTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	return fmt.Sprintf("code_cycles(%s)", path)
}
func (t *CodeCyclesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("code_cycles: invalid args: %w", err)
	}
	idx, err := codeMapIndex(ctx, t.Provider)
	if err != nil {
		return "", fmt.Errorf("code_cycles: %w", err)
	}
	max := normalizedCodeMapMax(a.MaxResults)
	return codemap.FormatCycles(idx.Cycles(a.Path, max), max), nil
}

// CodeMapDiagramTool returns a Mermaid dependency diagram. Set to_file to
// write the full, unbounded diagram to disk instead — a bounded-for-context-
// window / unbounded-on-disk export surface.
type CodeMapDiagramTool struct {
	Provider  codemap.Provider
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *CodeMapDiagramTool) Name() string { return "code_map_diagram" }
func (t *CodeMapDiagramTool) Description() string {
	return "Return a Mermaid dependency diagram from the experimental code map import graph, optionally focused around one file/path. Set to_file to write the full diagram to disk instead of returning it inline."
}
func (t *CodeMapDiagramTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "Optional file path or path fragment to focus the diagram"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on rendered edges (default 120, max 500)"},
		"to_file":     map[string]any{"type": "string", "description": "Optional path to write the full, untruncated diagram instead of returning it inline. Requires path — an unfocused export would be a full-repo hairball graph, which this tool deliberately never produces"},
	}}
}
func (t *CodeMapDiagramTool) RequiresApproval(argsJSON string) bool {
	return codeMapToFileArg(argsJSON) != ""
}
func (t *CodeMapDiagramTool) ParallelSafe(argsJSON string) bool {
	return codeMapToFileArg(argsJSON) == ""
}
func (t *CodeMapDiagramTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	if toFile := codeMapToFileArg(argsJSON); toFile != "" {
		return fmt.Sprintf("code_map_diagram(%s, to_file=%s)", path, toFile)
	}
	return fmt.Sprintf("code_map_diagram(%s)", path)
}
func (t *CodeMapDiagramTool) PathsToSnapshot(cwd, argsJSON string) []string {
	if toFile := codeMapToFileArg(argsJSON); toFile != "" {
		return []string{resolvePath(cwd, toFile)}
	}
	return nil
}
func (t *CodeMapDiagramTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
		ToFile     string `json:"to_file"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("code_map_diagram: invalid args: %w", err)
	}
	idx, err := codeMapIndex(ctx, t.Provider)
	if err != nil {
		return "", fmt.Errorf("code_map_diagram: %w", err)
	}
	if toFile := strings.TrimSpace(a.ToFile); toFile != "" {
		if strings.TrimSpace(a.Path) == "" {
			return "", fmt.Errorf("code_map_diagram: to_file requires path — an unfocused export would be the full import graph (a full-repo hairball diagram, which this tool never produces); pass path to focus the export around one file")
		}
		return writeCodeMapExport(t.Cwd, t.WriteOpts, toFile, codemap.MermaidDiagram(idx, a.Path, codeMapExportMax), "code_map_diagram")
	}
	return codemap.MermaidDiagram(idx, a.Path, normalizedCodeMapMax(a.MaxResults)), nil
}

func codeDependencySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "File path or path fragment to query"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned files (default 120, max 500)"},
	}, "required": []string{"path"}}
}

func codeImpactSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":          map[string]any{"type": "string", "description": "File path or path fragment to query"},
		"depth":         map[string]any{"type": "integer", "description": "Transitive dependent depth; -1 means all (default -1)"},
		"max_results":   map[string]any{"type": "integer", "description": "Cap on returned files/cycles (default 120, max 500)"},
		"format":        map[string]any{"type": "string", "description": "'full' (default) for the sectioned report, or 'summary' for a compact counts-plus-top-names projection sized for planning-turn context"},
		"include_calls": map[string]any{"type": "boolean", "description": "Best-effort supplement with live LSP call-hierarchy callers/callees for the target file's exported symbols when a language server is available (default false; slower than the static graph query)"},
	}, "required": []string{"path"}}
}

func previewCodeDependencyArgs(argsJSON string) (string, int) {
	var a struct {
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.Path, a.MaxResults
}

func executeCodeDependencyArgs(ctx context.Context, provider codemap.Provider, argsJSON, name string) (*codemap.CodeIndex, string, int, error) {
	var a struct {
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, "", 0, fmt.Errorf("%s: invalid args: %w", name, err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return nil, "", 0, fmt.Errorf("%s: path is required", name)
	}
	idx, err := codeMapIndex(ctx, provider)
	if err != nil {
		return nil, "", 0, fmt.Errorf("%s: %w", name, err)
	}
	return idx, a.Path, normalizedCodeMapMax(a.MaxResults), nil
}

func executeCodeImpactArgs(ctx context.Context, provider codemap.Provider, argsJSON, name string) (*codemap.CodeIndex, string, int, int, error) {
	var a struct {
		Path       string `json:"path"`
		Depth      int    `json:"depth"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, "", 0, 0, fmt.Errorf("%s: invalid args: %w", name, err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return nil, "", 0, 0, fmt.Errorf("%s: path is required", name)
	}
	depth := a.Depth
	if depth == 0 {
		depth = codemap.MaxDepthAll
	}
	idx, err := codeMapIndex(ctx, provider)
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("%s: %w", name, err)
	}
	return idx, a.Path, normalizedCodeMapMax(a.MaxResults), depth, nil
}

func codeMapArgs(ctx context.Context, provider codemap.Provider, argsJSON string) (*codemap.CodeIndex, string, int, error) {
	var a struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, "", 0, fmt.Errorf("code_map: invalid args: %w", err)
	}
	idx, err := codeMapIndex(ctx, provider)
	if err != nil {
		return nil, "", 0, fmt.Errorf("code_map: %w", err)
	}
	return idx, a.Query, normalizedCodeMapMax(a.MaxResults), nil
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

func normalizedCodeMapMax(n int) int {
	if n <= 0 {
		return defaultCodeMapMax
	}
	if n > 500 {
		return 500
	}
	return n
}
