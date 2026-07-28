package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// LSPChangedFilesDiagnosticsTool checks diagnostics for changed supported source
// files. It gives the agent one semantic post-edit check instead of requiring a
// separate lsp_diagnostics call for every touched file.
type LSPChangedFilesDiagnosticsTool struct{ lspToolBase }

func (t *LSPChangedFilesDiagnosticsTool) Name() string { return "lsp_changed_files_diagnostics" }
func (t *LSPChangedFilesDiagnosticsTool) Description() string {
	return "Return LSP diagnostics for git-changed supported source files after edits; use before declaring supported source changes done."
}
func (t *LSPChangedFilesDiagnosticsTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"max_files": map[string]any{"type": "integer", "description": "Maximum changed source files to inspect (default 20)"}}}
}
func (t *LSPChangedFilesDiagnosticsTool) RequiresApproval(string) bool { return false }
func (t *LSPChangedFilesDiagnosticsTool) ParallelSafe(string) bool     { return true }
func (t *LSPChangedFilesDiagnosticsTool) PreviewCall(string) string {
	return "lsp_changed_files_diagnostics()"
}
func (t *LSPChangedFilesDiagnosticsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		MaxFiles int `json:"max_files"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	maxFiles := a.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 20
	}
	paths, err := changedSourceFiles(ctx, t.cwd(), maxFiles)
	if err != nil {
		return "", fmt.Errorf("lsp_changed_files_diagnostics: %w", err)
	}
	if len(paths) == 0 {
		return "(no changed supported LSP source files)\n", nil
	}
	var b strings.Builder
	for _, path := range paths {
		client, abs, _, unavailable, err := openPositionClient(ctx, t.lspToolBase, fmt.Sprintf(`{"path":%q,"line":0,"character":0}`, path), "lsp_changed_files_diagnostics")
		if err != nil {
			return "", err
		}
		if unavailable != "" {
			b.WriteString(unavailable)
			continue
		}
		snap, err := client.Diagnostics(ctx, abs)
		_ = client.Close()
		if err != nil {
			return "", fmt.Errorf("lsp_changed_files_diagnostics: %w", err)
		}
		b.WriteString(formatDiagnosticsSnapshot(snap))
	}
	return b.String(), nil
}

func changedSourceFiles(ctx context.Context, cwd string, maxFiles int) ([]string, error) {
	repoRoot, err := gitTopLevel(ctx, cwd)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	collect := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoRoot
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			if _, ok := lspci.ResolveFile(line); !ok {
				continue
			}
			seen[line] = true
			paths = append(paths, filepath.ToSlash(line))
			if maxFiles > 0 && len(paths) >= maxFiles {
				return nil
			}
		}
		return nil
	}
	for _, args := range [][]string{{"diff", "--name-only", "HEAD", "--"}, {"diff", "--cached", "--name-only", "--"}, {"ls-files", "--others", "--exclude-standard"}} {
		if maxFiles > 0 && len(paths) >= maxFiles {
			break
		}
		if err := collect(args...); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func gitTopLevel(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
