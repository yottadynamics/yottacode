package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

type lspWorkspaceEditEnvelope struct {
	Edit lspci.WorkspaceEdit `json:"edit"`
}

// LSPRenamePreviewTool asks the language server for a semantic rename plan and
// prints the normalized WorkspaceEdit JSON. It intentionally does not mutate;
// applying the edit is a separate approval-gated tool call.
type LSPRenamePreviewTool struct{ lspToolBase }

func (t *LSPRenamePreviewTool) Name() string { return "lsp_rename_preview" }
func (t *LSPRenamePreviewTool) Description() string {
	return "Preview a language-server semantic rename as a WorkspaceEdit JSON payload without applying it."
}
func (t *LSPRenamePreviewTool) Schema() map[string]any {
	s := positionSchema()
	s["properties"].(map[string]any)["new_name"] = map[string]any{"type": "string", "description": "Replacement symbol name"}
	s["required"] = []string{"path", "line", "character", "new_name"}
	return s
}
func (t *LSPRenamePreviewTool) RequiresApproval(string) bool { return false }
func (t *LSPRenamePreviewTool) ParallelSafe(string) bool     { return true }
func (t *LSPRenamePreviewTool) PreviewCall(argsJSON string) string {
	var a struct{ Path, NewName string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_rename_preview(%s -> %s)", a.Path, a.NewName)
}
func (t *LSPRenamePreviewTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		positionArgs
		NewName string `json:"new_name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("lsp_rename_preview: invalid args: %w", err)
	}
	if strings.TrimSpace(a.NewName) == "" {
		return "", fmt.Errorf("lsp_rename_preview: new_name is required")
	}
	client, path, pos, unavailable, err := openPositionClient(ctx, t.lspToolBase, argsJSON, "lsp_rename_preview")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()
	edit, err := client.RenamePreview(ctx, path, pos, a.NewName)
	if errors.Is(err, lspci.ErrUnsupportedCapability) {
		return unsupportedCapabilityResult("lsp_rename_preview", err), nil
	}
	if err != nil {
		return "", fmt.Errorf("lsp_rename_preview: %w", err)
	}
	return formatWorkspaceEditPreview(edit)
}

// LSPFormatPreviewTool asks for formatting edits without applying them.
type LSPFormatPreviewTool struct{ lspToolBase }

func (t *LSPFormatPreviewTool) Name() string { return "lsp_format_preview" }
func (t *LSPFormatPreviewTool) Description() string {
	return "Preview language-server formatting edits for one file without applying them."
}
func (t *LSPFormatPreviewTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Source file path"}}, "required": []string{"path"}}
}
func (t *LSPFormatPreviewTool) RequiresApproval(string) bool { return false }
func (t *LSPFormatPreviewTool) ParallelSafe(string) bool     { return true }
func (t *LSPFormatPreviewTool) PreviewCall(argsJSON string) string {
	var a struct{ Path string }
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_format_preview(%s)", a.Path)
}
func (t *LSPFormatPreviewTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	client, _, _, unavailable, err := openPositionClient(ctx, t.lspToolBase, fmt.Sprintf(`%s`, argsJSON), "lsp_format_preview")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	path, _ := t.validateRead(a.Path)
	defer client.Close()
	edit, err := client.FormatPreview(ctx, path)
	if errors.Is(err, lspci.ErrUnsupportedCapability) {
		return unsupportedCapabilityResult("lsp_format_preview", err), nil
	}
	if err != nil {
		return "", fmt.Errorf("lsp_format_preview: %w", err)
	}
	return formatWorkspaceEditPreview(edit)
}

// LSPApplyWorkspaceEditTool applies a previously previewed WorkspaceEdit through
// yottacode's own write-path validator and checkpoint snapshot flow.
type LSPApplyWorkspaceEditTool struct {
	lspToolBase
	WriteOpts WritePathOptions
}

func (t *LSPApplyWorkspaceEditTool) Name() string { return "lsp_apply_workspace_edit" }
func (t *LSPApplyWorkspaceEditTool) Description() string {
	return "Apply a previously previewed LSP WorkspaceEdit JSON after path validation and approval."
}
func (t *LSPApplyWorkspaceEditTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"edit": map[string]any{"type": "object", "description": "WorkspaceEdit object returned by an LSP preview tool"}}, "required": []string{"edit"}}
}
func (t *LSPApplyWorkspaceEditTool) RequiresApproval(string) bool { return true }
func (t *LSPApplyWorkspaceEditTool) PreviewCall(argsJSON string) string {
	paths := t.PathsToSnapshot(t.cwd(), argsJSON)
	return fmt.Sprintf("lsp_apply_workspace_edit(%d files)", len(paths))
}
func (t *LSPApplyWorkspaceEditTool) PathsToSnapshot(cwd, argsJSON string) []string {
	edit, err := parseWorkspaceEditArg(argsJSON)
	if err != nil {
		return nil
	}
	paths := edit.Paths()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, resolvePath(cwd, path))
	}
	return out
}
func (t *LSPApplyWorkspaceEditTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	edit, err := parseWorkspaceEditArg(argsJSON)
	if err != nil {
		return "", fmt.Errorf("lsp_apply_workspace_edit: %w", err)
	}
	byPath := map[string][]lspci.TextEdit{}
	for _, e := range edit.Edits {
		abs := resolvePath(t.cwd(), e.Path)
		if err := ValidateWritePath(abs, t.WriteOpts); err != nil {
			return "", fmt.Errorf("lsp_apply_workspace_edit: %w", err)
		}
		byPath[abs] = append(byPath[abs], e)
	}
	var changed []string
	for path, edits := range byPath {
		oldBytes, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("lsp_apply_workspace_edit: read %s: %w", path, err)
		}
		newText, err := lspci.ApplyTextEdits(string(oldBytes), edits)
		if err != nil {
			return "", fmt.Errorf("lsp_apply_workspace_edit: %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
			return "", fmt.Errorf("lsp_apply_workspace_edit: write %s: %w", path, err)
		}
		changed = append(changed, path)
		_ = notifyLSPFileChanged(ctx, t.Cwd, t.Manager, t.Servers, path, newText)
	}
	return fmt.Sprintf("applied LSP workspace edit to %d file(s): %s", len(changed), strings.Join(changed, ", ")), nil
}

func parseWorkspaceEditArg(argsJSON string) (lspci.WorkspaceEdit, error) {
	var env lspWorkspaceEditEnvelope
	if err := json.Unmarshal([]byte(argsJSON), &env); err != nil {
		return lspci.WorkspaceEdit{}, err
	}
	if len(env.Edit.Edits) == 0 {
		return lspci.WorkspaceEdit{}, fmt.Errorf("edit contains no text edits")
	}
	return env.Edit, nil
}

func formatWorkspaceEditPreview(edit lspci.WorkspaceEdit) (string, error) {
	payload, err := json.MarshalIndent(lspWorkspaceEditEnvelope{Edit: edit}, "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(lspci.WorkspaceEditSummary(edit))
	for _, path := range edit.Paths() {
		oldBytes, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(&b, "preview\t%s\tread error: %v\n", filepath.Base(path), err)
			continue
		}
		var edits []lspci.TextEdit
		for _, edit := range edit.Edits {
			if edit.Path == path {
				edits = append(edits, edit)
			}
		}
		newText, err := lspci.ApplyTextEdits(string(oldBytes), edits)
		if err != nil {
			fmt.Fprintf(&b, "preview\t%s\tapply error: %v\n", filepath.Base(path), err)
			continue
		}
		b.WriteString(simpleUnifiedDiff(path, string(oldBytes), newText))
	}
	b.WriteString("\napply_payload:\n")
	b.Write(payload)
	b.WriteByte('\n')
	return b.String(), nil
}

func simpleUnifiedDiff(path, oldText, newText string) string {
	if oldText == newText {
		return fmt.Sprintf("diff -- %s\n(no changes)\n", path)
	}
	oldLines := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")
	newLines := strings.Split(strings.TrimSuffix(newText, "\n"), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	for _, line := range oldLines {
		fmt.Fprintf(&b, "-%s\n", line)
	}
	for _, line := range newLines {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return b.String()
}
