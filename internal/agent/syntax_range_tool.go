package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// SyntaxRangeTool exposes parser-backed enclosing ranges without starting a
// language server. Agents use the returned anchor_read hints to re-read the
// chosen range with anchors before applying edit_anchored.
type SyntaxRangeTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *SyntaxRangeTool) Name() string { return "syntax_range" }
func (t *SyntaxRangeTool) Description() string {
	return "Return offline parser-backed syntax ranges around a source position, plus anchored-read hints for safe block edits."
}
func (t *SyntaxRangeTool) Schema() map[string]any {
	s := positionSchema()
	s["properties"].(map[string]any)["max_results"] = map[string]any{"type": "integer", "description": "Cap on returned ranges (default 50)"}
	return s
}
func (t *SyntaxRangeTool) RequiresApproval(string) bool { return false }
func (t *SyntaxRangeTool) ParallelSafe(string) bool     { return true }
func (t *SyntaxRangeTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("syntax_range(%s:%d:%d)", a.Path, a.Line, a.Character)
}

func (t *SyntaxRangeTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a positionArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("syntax_range: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("syntax_range: path is required")
	}
	path := resolvePath(t.cwd(), a.Path)
	if err := ValidateReadPath(path, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("syntax_range: %w", err)
	}
	lang, ok := lspci.ResolveFile(path)
	if !ok {
		return fmt.Sprintf("unavailable: syntax ranges are not available for %s (supported parser-backed ranges: Go)\n", path), nil
	}
	ranges, ok, err := lspci.SyntaxFileRanges(ctx, lang, path, lspci.Position{Line: a.Line, Character: a.Character})
	if err != nil {
		return "", fmt.Errorf("syntax_range: %w", err)
	}
	if !ok {
		return fmt.Sprintf("unavailable: syntax ranges are not available for %s; use lsp_selection_ranges if a server is installed\n", lang.Name), nil
	}
	if len(ranges) == 0 {
		return "(no syntax ranges)\n", nil
	}
	limit := normalizedLSPMax(a.MaxResults)
	var b strings.Builder
	for i, r := range ranges {
		if i >= limit {
			fmt.Fprintf(&b, "…[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintf(&b, "%s\t%s\tlines=%d-%d\tanchor_read=%s", syntaxRangeLabel(r), displayRange(path, r.Range), r.Range.Start.Line+1, r.Range.End.Line+1, anchorReadHint(path, r.Range))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func (t *SyntaxRangeTool) cwd() string {
	if t.Cwd == nil {
		return "."
	}
	return t.Cwd.Get()
}

func syntaxRangeLabel(r lspci.SyntaxRange) string {
	label := r.Kind
	if strings.TrimSpace(r.Name) != "" {
		label += " " + strings.TrimSpace(r.Name)
	}
	if strings.TrimSpace(r.Detail) != "" {
		label += " [" + strings.TrimSpace(r.Detail) + "]"
	}
	return label
}

func anchorReadHint(path string, r lspci.TextRange) string {
	start := r.Start.Line + 1
	end := r.End.Line + 1
	if end < start {
		end = start
	}
	limit := end - start + 1
	if limit < 1 {
		limit = 1
	}
	rel := filepath.ToSlash(path)
	b, err := json.Marshal(map[string]any{"path": rel, "offset": start, "limit": limit, "anchors": true})
	if err != nil {
		return "{}"
	}
	return string(b)
}
