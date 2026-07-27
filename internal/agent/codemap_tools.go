package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

const defaultCodeMapMax = 120

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

// CodeStructureProjectionTool returns a compact context projection for agents.
type CodeStructureProjectionTool struct{ Provider codemap.Provider }

func (t *CodeStructureProjectionTool) Name() string { return "code_structure_projection" }
func (t *CodeStructureProjectionTool) Description() string {
	return "Generate a compact, token-efficient structure projection from the code index: package/file tree, key symbols, and counts."
}
func (t *CodeStructureProjectionTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"max_results": map[string]any{"type": "integer", "description": "Cap on files included (default 120, max 500)"},
	}}
}
func (t *CodeStructureProjectionTool) RequiresApproval(string) bool { return false }
func (t *CodeStructureProjectionTool) ParallelSafe(string) bool     { return true }
func (t *CodeStructureProjectionTool) PreviewCall(string) string {
	return "code_structure_projection()"
}
func (t *CodeStructureProjectionTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	idx, _, max, err := codeMapArgs(ctx, t.Provider, argsJSON)
	if err != nil {
		return "", err
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

type CodeImpactTool struct{ Provider codemap.Provider }

func (t *CodeImpactTool) Name() string { return "code_impact" }
func (t *CodeImpactTool) Description() string {
	return "Return a conservative blast-radius summary for an indexed file/path query: direct dependencies, direct dependents, transitive dependents, and import cycles."
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
	return codemap.FormatImpact(idx.Impact(path, depth, max), max), nil
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

type CodeMapDiagramTool struct{ Provider codemap.Provider }

func (t *CodeMapDiagramTool) Name() string { return "code_map_diagram" }
func (t *CodeMapDiagramTool) Description() string {
	return "Return a Mermaid dependency diagram from the experimental code map import graph, optionally focused around one file/path."
}
func (t *CodeMapDiagramTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "Optional file path or path fragment to focus the diagram"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on rendered edges (default 120, max 500)"},
	}}
}
func (t *CodeMapDiagramTool) RequiresApproval(string) bool { return false }
func (t *CodeMapDiagramTool) ParallelSafe(string) bool     { return true }
func (t *CodeMapDiagramTool) PreviewCall(argsJSON string) string {
	path, _ := previewCodeDependencyArgs(argsJSON)
	return fmt.Sprintf("code_map_diagram(%s)", path)
}
func (t *CodeMapDiagramTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("code_map_diagram: invalid args: %w", err)
	}
	idx, err := codeMapIndex(ctx, t.Provider)
	if err != nil {
		return "", fmt.Errorf("code_map_diagram: %w", err)
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
		"path":        map[string]any{"type": "string", "description": "File path or path fragment to query"},
		"depth":       map[string]any{"type": "integer", "description": "Transitive dependent depth; -1 means all (default -1)"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned files/cycles (default 120, max 500)"},
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
