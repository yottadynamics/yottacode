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
	results := make([]changedFileDiagnosticsResult, 0, len(paths))
	for _, path := range paths {
		client, abs, _, unavailable, err := openPositionClient(ctx, t.lspToolBase, fmt.Sprintf(`{"path":%q,"line":0,"character":0}`, path), "lsp_changed_files_diagnostics")
		if err != nil {
			return "", err
		}
		if unavailable != "" {
			results = append(results, changedFileDiagnosticsResult{Path: path, Unavailable: strings.TrimSpace(unavailable)})
			continue
		}
		snap, err := client.Diagnostics(ctx, abs)
		_ = client.Close()
		if err != nil {
			return "", fmt.Errorf("lsp_changed_files_diagnostics: %w", err)
		}
		results = append(results, changedFileDiagnosticsResult{Path: path, Snapshot: snap})
	}
	return formatChangedFilesDiagnostics(results), nil
}

type changedFileDiagnosticsResult struct {
	Path        string
	Snapshot    lspci.DiagnosticsSnapshot
	Unavailable string
}

// formatChangedFilesDiagnostics turns many per-file LSP responses into one
// user-facing summary. A clean multi-file check should read as one clean result,
// not as dozens of repeated "no diagnostics" lines.
func formatChangedFilesDiagnostics(results []changedFileDiagnosticsResult) string {
	if len(results) == 0 {
		return "(no changed supported LSP source files)\n"
	}

	var (
		b              strings.Builder
		cleanFiles     int
		unpublished    []string
		unavailable    []changedFileDiagnosticsResult
		diagnosticRows []string
		issueFiles     int
		issueCount     int
	)

	for _, result := range results {
		switch {
		case result.Unavailable != "":
			unavailable = append(unavailable, result)
		case !result.Snapshot.Published:
			unpublished = append(unpublished, result.Path)
		case len(result.Snapshot.Diagnostics) == 0:
			cleanFiles++
		default:
			issueFiles++
			issueCount += len(result.Snapshot.Diagnostics)
			diagnosticRows = append(diagnosticRows, strings.TrimRight(formatDiagnosticsSnapshot(result.Snapshot), "\n"))
		}
	}

	fmt.Fprintf(&b, "checked %d changed LSP source %s\n", len(results), plural(len(results), "file", "files"))
	if issueCount == 0 && len(unpublished) == 0 && len(unavailable) == 0 {
		fmt.Fprintf(&b, "✓ clean: no diagnostics in %d %s\n", cleanFiles, plural(cleanFiles, "file", "files"))
		b.WriteString("files: ")
		b.WriteString(formatPathList(resultPaths(results), 5))
		b.WriteByte('\n')
		return b.String()
	}

	if issueCount > 0 {
		fmt.Fprintf(&b, "⚠ %d %s in %d %s\n", issueCount, plural(issueCount, "diagnostic", "diagnostics"), issueFiles, plural(issueFiles, "file", "files"))
	}
	if cleanFiles > 0 {
		fmt.Fprintf(&b, "✓ clean: %d %s\n", cleanFiles, plural(cleanFiles, "file", "files"))
	}
	if len(unpublished) > 0 {
		fmt.Fprintf(&b, "○ pending: diagnostics not published for %d %s before timeout\n", len(unpublished), plural(len(unpublished), "file", "files"))
		fmt.Fprintf(&b, "pending files: %s\n", formatPathList(unpublished, 5))
	}
	if len(unavailable) > 0 {
		fmt.Fprintf(&b, "○ skipped: LSP unavailable for %d %s\n", len(unavailable), plural(len(unavailable), "file", "files"))
		for _, result := range unavailable {
			fmt.Fprintf(&b, "%s: %s\n", result.Path, result.Unavailable)
		}
	}
	for _, row := range diagnosticRows {
		if row == "" {
			continue
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return b.String()
}

func resultPaths(results []changedFileDiagnosticsResult) []string {
	paths := make([]string, 0, len(results))
	for _, result := range results {
		paths = append(paths, result.Path)
	}
	return paths
}

func formatPathList(paths []string, limit int) string {
	if len(paths) == 0 {
		return "(none)"
	}
	if limit <= 0 || len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, …%d more", strings.Join(paths[:limit], ", "), len(paths)-limit)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
			// Ignore generated cache/config trees before extension resolution so
			// a polluted worktree does not crowd out real changed source files.
			if _, ok := generatedArtifactRoot(line); ok {
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
