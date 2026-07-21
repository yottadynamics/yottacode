package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

const defaultLSPMaxResults = 50

// lspClient is the narrow language-server surface the agent tools need. Tests
// inject fakes through lspClientFactory so they never require real LSP binaries.
type lspClient interface {
	WorkspaceSymbols(ctx context.Context, query string) ([]lspci.Symbol, error)
	Definition(ctx context.Context, path string, pos lspci.Position) ([]lspci.Location, error)
	References(ctx context.Context, path string, pos lspci.Position, includeDeclaration bool) ([]lspci.Location, error)
	Hover(ctx context.Context, path string, pos lspci.Position) (string, error)
	Diagnostics(ctx context.Context, path string) ([]lspci.Diagnostic, error)
	CodeActions(ctx context.Context, path string, start, end lspci.Position) ([]lspci.CodeAction, error)
	CallHierarchy(ctx context.Context, path string, pos lspci.Position) ([]lspci.CallHierarchyItem, error)
	Close() error
}

type lspClientFactory func(ctx context.Context, lang lspci.Language, root string) (lspClient, error)

func defaultLSPClientFactory(ctx context.Context, lang lspci.Language, root string) (lspClient, error) {
	return lspci.NewClient(ctx, lang, root)
}

type lspToolBase struct {
	Cwd           *CwdRef
	DenyReadPaths []string
	NewClient     lspClientFactory
	Servers       map[string][]string
}

func (b lspToolBase) cwd() string {
	if b.Cwd == nil {
		return "."
	}
	return b.Cwd.Get()
}

func (b lspToolBase) factory() lspClientFactory {
	if b.NewClient != nil {
		return b.NewClient
	}
	return defaultLSPClientFactory
}

func (b lspToolBase) validateRead(path string) (string, error) {
	abs := resolvePath(b.cwd(), path)
	if err := ValidateReadPath(abs, b.DenyReadPaths); err != nil {
		return "", err
	}
	return abs, nil
}

// LSPStatusTool reports language-server readiness for the current workspace.
type LSPStatusTool struct{ lspToolBase }

func (t *LSPStatusTool) Name() string { return "lsp_status" }
func (t *LSPStatusTool) Description() string {
	return "Detect supported source languages in the workspace and report the language server command, installed/missing status, and install hints needed for LSP code intelligence."
}
func (t *LSPStatusTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "Workspace path to scan (default: cwd)"},
	}}
}
func (t *LSPStatusTool) RequiresApproval(string) bool { return false }
func (t *LSPStatusTool) ParallelSafe(string) bool     { return true }
func (t *LSPStatusTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Path == "" {
		a.Path = "."
	}
	return fmt.Sprintf("lsp_status(%s)", a.Path)
}
func (t *LSPStatusTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("lsp_status: invalid args: %w", err)
	}
	root := a.Path
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := t.validateRead(root)
	if err != nil {
		return "", fmt.Errorf("lsp_status: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("lsp_status: %w", err)
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	langs, err := lspci.DetectWorkspace(ctx, abs, 2000)
	if err != nil {
		return "", fmt.Errorf("lsp_status: %w", err)
	}
	langs = lspci.ApplyOverridesToDetected(langs, t.Servers)
	if len(langs) == 0 {
		return "no supported LSP languages detected (supported: Go, TypeScript/JavaScript, Python, Rust)\n", nil
	}
	var b strings.Builder
	for _, lang := range langs {
		status := "missing"
		if lang.ServerAvailable {
			status = "installed"
		}
		fmt.Fprintf(&b, "%s\tfiles=%d\tserver=%s\tstatus=%s", lang.Name, lang.FilesAvailable, strings.Join(lang.Command, " "), status)
		if !lang.ServerAvailable {
			fmt.Fprintf(&b, "\thint=%s", lang.InstallHint)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// LSPSymbolsTool searches workspace symbols through the resolved server.
type LSPSymbolsTool struct{ lspToolBase }

func (t *LSPSymbolsTool) Name() string { return "lsp_symbols" }
func (t *LSPSymbolsTool) Description() string {
	return "Search language-server workspace symbols for supported languages. Requires the matching language server to be installed on PATH; use lsp_status for install hints."
}
func (t *LSPSymbolsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query":       map[string]any{"type": "string", "description": "Symbol search query"},
		"path":        map[string]any{"type": "string", "description": "File or workspace path used to infer the language (default: workspace auto-detect)"},
		"max_results": map[string]any{"type": "integer", "description": "Cap on returned symbols (default 50)"},
	}, "required": []string{"query"}}
}
func (t *LSPSymbolsTool) RequiresApproval(string) bool { return false }
func (t *LSPSymbolsTool) ParallelSafe(string) bool     { return true }
func (t *LSPSymbolsTool) PreviewCall(argsJSON string) string {
	var a struct{ Query, Path string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_symbols(%q in %s)", a.Query, emptyDefault(a.Path, "."))
}
func (t *LSPSymbolsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Query      string `json:"query"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("lsp_symbols: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("lsp_symbols: query is required")
	}
	lang, root, unavailable, err := t.resolveLanguage(ctx, a.Path)
	if err != nil {
		return "", err
	}
	if unavailable == "fallback" {
		items, fbErr := lspci.FallbackSymbols(ctx, root, a.Query, 2000)
		if fbErr != nil {
			return "", fmt.Errorf("lsp_symbols: fallback: %w", fbErr)
		}
		return formatSymbols(items, normalizedLSPMax(a.MaxResults), "fallback: no language server installed; using approximate regex index\n"), nil
	}
	if unavailable != "" {
		return unavailable, nil
	}
	client, err := t.factory()(ctx, lang, root)
	if err != nil {
		return missingServerResult(lang, err), nil
	}
	defer client.Close()
	items, err := client.WorkspaceSymbols(ctx, a.Query)
	if err != nil {
		if t.NewClient == nil {
			fallback, fbErr := lspci.FallbackSymbols(ctx, root, a.Query, 2000)
			if fbErr == nil && len(fallback) > 0 {
				return formatSymbols(fallback, normalizedLSPMax(a.MaxResults), "fallback: language-server workspace symbols failed; using approximate regex index\n"), nil
			}
		}
		return "", fmt.Errorf("lsp_symbols: %w", err)
	}
	return formatSymbols(items, normalizedLSPMax(a.MaxResults), ""), nil
}

// LSPDefinitionTool returns definition locations for a source position.
type LSPDefinitionTool struct{ lspToolBase }

func (t *LSPDefinitionTool) Name() string { return "lsp_definition" }
func (t *LSPDefinitionTool) Description() string {
	return "Find the language-server definition location for a file position (zero-based line and character). Requires the matching server on PATH."
}
func (t *LSPDefinitionTool) Schema() map[string]any       { return positionSchema() }
func (t *LSPDefinitionTool) RequiresApproval(string) bool { return false }
func (t *LSPDefinitionTool) ParallelSafe(string) bool     { return true }
func (t *LSPDefinitionTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_definition(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPDefinitionTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	return executeLSPLocations(ctx, t.lspToolBase, argsJSON, "lsp_definition", false)
}

// LSPReferencesTool returns reference locations for a source position.
type LSPReferencesTool struct{ lspToolBase }

func (t *LSPReferencesTool) Name() string { return "lsp_references" }
func (t *LSPReferencesTool) Description() string {
	return "Find language-server references for a file position (zero-based line and character). Requires the matching server on PATH."
}
func (t *LSPReferencesTool) Schema() map[string]any {
	s := positionSchema()
	s["properties"].(map[string]any)["include_declaration"] = map[string]any{"type": "boolean", "description": "Include the declaration in the references (default false)"}
	s["properties"].(map[string]any)["max_results"] = map[string]any{"type": "integer", "description": "Cap on returned references (default 50)"}
	return s
}
func (t *LSPReferencesTool) RequiresApproval(string) bool { return false }
func (t *LSPReferencesTool) ParallelSafe(string) bool     { return true }
func (t *LSPReferencesTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_references(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPReferencesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	return executeLSPLocations(ctx, t.lspToolBase, argsJSON, "lsp_references", true)
}

type positionArgs struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Character          int    `json:"character"`
	IncludeDeclaration bool   `json:"include_declaration"`
	MaxResults         int    `json:"max_results"`
}

func positionSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":      map[string]any{"type": "string", "description": "Source file path"},
		"line":      map[string]any{"type": "integer", "description": "Zero-based line"},
		"character": map[string]any{"type": "integer", "description": "Zero-based UTF-16 character offset"},
	}, "required": []string{"path", "line", "character"}}
}

func executeLSPLocations(ctx context.Context, base lspToolBase, argsJSON, name string, references bool) (string, error) {
	var a positionArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("%s: invalid args: %w", name, err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("%s: path is required", name)
	}
	path, err := base.validateRead(a.Path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	lang, ok := lspci.ResolveFile(path)
	if !ok {
		return unsupportedFileResult(path), nil
	}
	lang = lspci.ApplyOverrides(lang, base.Servers)
	if !lspci.ServerAvailable(lang) && base.NewClient == nil {
		return unavailableServerResult(lang), nil
	}
	client, err := base.factory()(ctx, lang, base.cwd())
	if err != nil {
		return missingServerResult(lang, err), nil
	}
	defer client.Close()
	pos := lspci.Position{Line: a.Line, Character: a.Character}
	var locs []lspci.Location
	if references {
		locs, err = client.References(ctx, path, pos, a.IncludeDeclaration)
	} else {
		locs, err = client.Definition(ctx, path, pos)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if len(locs) == 0 {
		return "(no locations)\n", nil
	}
	limit := normalizedLSPMax(a.MaxResults)
	var b strings.Builder
	for i, loc := range locs {
		if i >= limit {
			fmt.Fprintf(&b, "…[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintln(&b, displayLocation(loc))
	}
	return b.String(), nil
}

func (t *LSPSymbolsTool) resolveLanguage(ctx context.Context, path string) (lspci.Language, string, string, error) {
	root := t.cwd()
	if strings.TrimSpace(path) != "" {
		abs, err := t.validateRead(path)
		if err != nil {
			return lspci.Language{}, "", "", fmt.Errorf("lsp_symbols: %w", err)
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			if lang, ok := lspci.ResolveFile(abs); ok {
				return lspci.ApplyOverrides(lang, t.Servers), filepath.Dir(abs), "", nil
			}
			return lspci.Language{}, "", unsupportedFileResult(abs), nil
		}
		root = abs
	}
	langs, err := lspci.DetectWorkspace(ctx, root, 2000)
	if err != nil {
		return lspci.Language{}, "", "", fmt.Errorf("lsp_symbols: %w", err)
	}
	langs = lspci.ApplyOverridesToDetected(langs, t.Servers)
	if len(langs) == 0 {
		return lspci.Language{}, "", "no supported LSP languages detected (supported: Go, TypeScript/JavaScript, Python, Rust)\n", nil
	}
	for _, detected := range langs {
		if detected.ServerAvailable || t.NewClient != nil {
			return detected.Language, root, "", nil
		}
	}
	if fallback, err := lspci.FallbackSymbols(ctx, root, "", 2000); err == nil && len(fallback) > 0 {
		return langs[0].Language, root, "fallback", nil
	}
	return lspci.Language{}, "", unavailableServerResult(langs[0].Language), nil
}

func normalizedLSPMax(n int) int {
	if n <= 0 {
		return defaultLSPMaxResults
	}
	if n > 500 {
		return 500
	}
	return n
}

func displayLocation(loc lspci.Location) string {
	return fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, loc.Character+1)
}

func unavailableServerResult(lang lspci.Language) string {
	return fmt.Sprintf("unavailable: %s language server %q not found on PATH. %s\n", lang.Name, lang.Command[0], lang.InstallHint)
}

func missingServerResult(lang lspci.Language, err error) string {
	return fmt.Sprintf("unavailable: %s language server could not start: %v. %s\n", lang.Name, err, lang.InstallHint)
}

func unsupportedFileResult(path string) string {
	return fmt.Sprintf("unavailable: no supported LSP language for %s (supported: Go, TypeScript/JavaScript, Python, Rust)\n", path)
}

func emptyDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
