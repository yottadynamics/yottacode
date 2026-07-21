package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

import lspci "github.com/yottadynamics/yottacode/internal/lsp"

// LSPHoverTool returns hover/type information for a source position.
type LSPHoverTool struct{ lspToolBase }

func (t *LSPHoverTool) Name() string { return "lsp_hover" }
func (t *LSPHoverTool) Description() string {
	return "Show language-server hover/type/documentation information for a file position (zero-based line and character)."
}
func (t *LSPHoverTool) Schema() map[string]any       { return positionSchema() }
func (t *LSPHoverTool) RequiresApproval(string) bool { return false }
func (t *LSPHoverTool) ParallelSafe(string) bool     { return true }
func (t *LSPHoverTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_hover(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPHoverTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	client, path, pos, unavailable, err := openPositionClient(ctx, t.lspToolBase, argsJSON, "lsp_hover")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()
	text, err := client.Hover(ctx, path, pos)
	if err != nil {
		return "", fmt.Errorf("lsp_hover: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return "(no hover)\n", nil
	}
	return text + "\n", nil
}

// LSPDiagnosticsTool returns language-server diagnostics for one source file.
type LSPDiagnosticsTool struct{ lspToolBase }

func (t *LSPDiagnosticsTool) Name() string { return "lsp_diagnostics" }
func (t *LSPDiagnosticsTool) Description() string {
	return "Return compile/type diagnostics from the matching language server for a supported source file."
}
func (t *LSPDiagnosticsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Source file path"}}, "required": []string{"path"}}
}
func (t *LSPDiagnosticsTool) RequiresApproval(string) bool { return false }
func (t *LSPDiagnosticsTool) ParallelSafe(string) bool     { return true }
func (t *LSPDiagnosticsTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_diagnostics(%s)", a.Path)
}
func (t *LSPDiagnosticsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("lsp_diagnostics: invalid args: %w", err)
	}
	client, path, _, unavailable, err := openPositionClient(ctx, t.lspToolBase, fmt.Sprintf(`{"path":%q,"line":0,"character":0}`, a.Path), "lsp_diagnostics")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()
	diags, err := client.Diagnostics(ctx, path)
	if err != nil {
		return "", fmt.Errorf("lsp_diagnostics: %w", err)
	}
	if len(diags) == 0 {
		return "(no diagnostics)\n", nil
	}
	var b strings.Builder
	for _, d := range diags {
		source := d.Source
		if source != "" {
			source = "\t" + source
		}
		fmt.Fprintf(&b, "%s:%d:%d\t%s%s\t%s\n", d.Path, d.Line+1, d.Character+1, d.Severity, source, d.Message)
	}
	return b.String(), nil
}

// LSPCodeActionsTool lists available code actions without applying them.
type LSPCodeActionsTool struct{ lspToolBase }

func (t *LSPCodeActionsTool) Name() string { return "lsp_code_actions" }
func (t *LSPCodeActionsTool) Description() string {
	return "List language-server code actions/quick fixes for a file range without applying them."
}
func (t *LSPCodeActionsTool) Schema() map[string]any {
	s := positionSchema()
	props := s["properties"].(map[string]any)
	props["end_line"] = map[string]any{"type": "integer", "description": "Zero-based end line (default: line)"}
	props["end_character"] = map[string]any{"type": "integer", "description": "Zero-based end UTF-16 character offset (default: character)"}
	return s
}
func (t *LSPCodeActionsTool) RequiresApproval(string) bool { return false }
func (t *LSPCodeActionsTool) ParallelSafe(string) bool     { return true }
func (t *LSPCodeActionsTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_code_actions(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPCodeActionsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		positionArgs
		EndLine      int `json:"end_line"`
		EndCharacter int `json:"end_character"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	client, path, pos, unavailable, err := openPositionClient(ctx, t.lspToolBase, argsJSON, "lsp_code_actions")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()
	end := lspci.Position{Line: a.EndLine, Character: a.EndCharacter}
	if end.Line == 0 && end.Character == 0 {
		end = pos
	}
	actions, err := client.CodeActions(ctx, path, pos, end)
	if err != nil {
		return "", fmt.Errorf("lsp_code_actions: %w", err)
	}
	if len(actions) == 0 {
		return "(no code actions)\n", nil
	}
	var b strings.Builder
	for _, action := range actions {
		fmt.Fprintf(&b, "%s\t%s\n", action.Kind, action.Title)
	}
	return b.String(), nil
}

// LSPCallHierarchyTool returns incoming/outgoing calls for a source position.
type LSPCallHierarchyTool struct{ lspToolBase }

func (t *LSPCallHierarchyTool) Name() string { return "lsp_call_hierarchy" }
func (t *LSPCallHierarchyTool) Description() string {
	return "Show incoming and outgoing call hierarchy for a file position through the matching language server."
}
func (t *LSPCallHierarchyTool) Schema() map[string]any       { return positionSchema() }
func (t *LSPCallHierarchyTool) RequiresApproval(string) bool { return false }
func (t *LSPCallHierarchyTool) ParallelSafe(string) bool     { return true }
func (t *LSPCallHierarchyTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_call_hierarchy(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPCallHierarchyTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	client, path, pos, unavailable, err := openPositionClient(ctx, t.lspToolBase, argsJSON, "lsp_call_hierarchy")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()
	items, err := client.CallHierarchy(ctx, path, pos)
	if err != nil {
		return "", fmt.Errorf("lsp_call_hierarchy: %w", err)
	}
	if len(items) == 0 {
		return "(no call hierarchy)\n", nil
	}
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", item.Direction, displayLocation(item.Location), item.Kind, item.Name, item.Detail)
	}
	return b.String(), nil
}

func openPositionClient(ctx context.Context, base lspToolBase, argsJSON, name string) (lspClient, string, lspci.Position, string, error) {
	var a positionArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, "", lspci.Position{}, "", fmt.Errorf("%s: invalid args: %w", name, err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return nil, "", lspci.Position{}, "", fmt.Errorf("%s: path is required", name)
	}
	path, err := base.validateRead(a.Path)
	if err != nil {
		return nil, "", lspci.Position{}, "", fmt.Errorf("%s: %w", name, err)
	}
	lang, ok := lspci.ResolveFile(path)
	if !ok {
		return nil, path, lspci.Position{}, unsupportedFileResult(path), nil
	}
	lang = lspci.ApplyOverrides(lang, base.Servers)
	if !lspci.ServerAvailable(lang) && base.NewClient == nil {
		return nil, path, lspci.Position{}, unavailableServerResult(lang), nil
	}
	client, err := base.factory()(ctx, lang, base.cwd())
	if err != nil {
		return nil, path, lspci.Position{}, missingServerResult(lang, err), nil
	}
	return client, path, lspci.Position{Line: a.Line, Character: a.Character}, "", nil
}

func formatSymbols(items []lspci.Symbol, limit int, prefix string) string {
	if len(items) == 0 {
		return "(no symbols)\n"
	}
	var b strings.Builder
	b.WriteString(prefix)
	for i, item := range items {
		if i >= limit {
			fmt.Fprintf(&b, "…[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", displayLocation(item.Location), item.Kind, item.Name, item.Container)
	}
	return b.String()
}
