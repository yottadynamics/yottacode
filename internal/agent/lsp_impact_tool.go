package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// LSPImpactTool combines several semantic queries into one compact blast-radius
// report. It is agent-facing rather than raw-LSP-facing: callers get hover,
// definitions, references, calls, diagnostics, and optional Code Map import
// impact without spending a tool round on each primitive.
type LSPImpactTool struct {
	lspToolBase
	CodeMapProvider codemap.Provider
}

func (t *LSPImpactTool) Name() string { return "lsp_impact" }
func (t *LSPImpactTool) Description() string {
	return "Return an agent-native LSP impact report for a source position: hover, definitions, references, calls, diagnostics, and optional Code Map import blast radius."
}
func (t *LSPImpactTool) Schema() map[string]any {
	s := positionSchema()
	props := s["properties"].(map[string]any)
	props["include_declaration"] = map[string]any{"type": "boolean", "description": "Include the declaration in reference results (default false)"}
	props["max_results"] = map[string]any{"type": "integer", "description": "Cap per section (default 50, max 500)"}
	return s
}
func (t *LSPImpactTool) RequiresApproval(string) bool { return false }

// lsp_impact holds one LSP client across multiple sequential JSON-RPC requests,
// so the scheduler should not run it concurrently with other LSP work.
func (t *LSPImpactTool) ParallelSafe(string) bool { return false }
func (t *LSPImpactTool) PreviewCall(argsJSON string) string {
	var a positionArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("lsp_impact(%s:%d:%d)", a.Path, a.Line, a.Character)
}
func (t *LSPImpactTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a positionArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("lsp_impact: invalid args: %w", err)
	}
	client, path, pos, unavailable, err := openPositionClient(ctx, t.lspToolBase, argsJSON, "lsp_impact")
	if err != nil || unavailable != "" {
		return unavailable, err
	}
	defer client.Close()

	limit := normalizedLSPMax(a.MaxResults)
	var b strings.Builder
	fmt.Fprintf(&b, "impact\t%s:%d:%d\n", path, pos.Line+1, pos.Character+1)
	writeImpactHover(ctx, &b, client, path, pos)
	writeImpactLocations(ctx, &b, "definitions", func() ([]lspci.Location, error) { return client.Definition(ctx, path, pos) }, limit)
	writeImpactLocations(ctx, &b, "references", func() ([]lspci.Location, error) { return client.References(ctx, path, pos, a.IncludeDeclaration) }, limit)
	writeImpactCalls(ctx, &b, client, path, pos, limit)
	writeImpactDiagnostics(ctx, &b, client, path)
	writeImpactCodeMap(ctx, &b, t.CodeMapProvider, path, limit)
	return b.String(), nil
}

func writeImpactHover(ctx context.Context, b *strings.Builder, client lspClient, path string, pos lspci.Position) {
	text, err := client.Hover(ctx, path, pos)
	b.WriteString("hover\n")
	if err != nil {
		writeImpactUnavailable(b, err)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.WriteString("  (none)\n")
		return
	}
	text = strings.ReplaceAll(text, "\n", " ")
	fmt.Fprintf(b, "  %s\n", truncateImpactText(text, 240))
}

func writeImpactLocations(ctx context.Context, b *strings.Builder, title string, request func() ([]lspci.Location, error), limit int) {
	b.WriteString(title + "\n")
	locs, err := request()
	if err != nil {
		writeImpactUnavailable(b, err)
		return
	}
	if len(locs) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for i, loc := range locs {
		if i >= limit {
			fmt.Fprintf(b, "  …[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintf(b, "  %s\n", displayLocation(loc))
	}
	_ = ctx
}

func writeImpactCalls(ctx context.Context, b *strings.Builder, client lspClient, path string, pos lspci.Position, limit int) {
	b.WriteString("calls\n")
	items, err := client.CallHierarchy(ctx, path, pos)
	if err != nil {
		writeImpactUnavailable(b, err)
		return
	}
	if len(items) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for i, item := range items {
		if i >= limit {
			fmt.Fprintf(b, "  …[truncated at %d results]\n", limit)
			break
		}
		fmt.Fprintf(b, "  %s\t%s\t%s\t%s\n", item.Direction, displayLocation(item.Location), item.Kind, item.Name)
	}
}

func writeImpactDiagnostics(ctx context.Context, b *strings.Builder, client lspClient, path string) {
	b.WriteString("diagnostics\n")
	snap, err := client.Diagnostics(ctx, path)
	if err != nil {
		writeImpactUnavailable(b, err)
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(formatDiagnosticsSnapshot(snap), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
}

func writeImpactCodeMap(ctx context.Context, b *strings.Builder, provider codemap.Provider, path string, limit int) {
	b.WriteString("code_map_import_impact\n")
	if provider == nil {
		b.WriteString("  unavailable: code map is not enabled\n")
		return
	}
	idx, err := provider.Index(ctx)
	if err != nil || idx == nil {
		if err == nil {
			err = errors.New("code map index is unavailable")
		}
		fmt.Fprintf(b, "  unavailable: %v\n", err)
		return
	}
	query := path
	if root := idx.RootPath(); root != "" && filepath.IsAbs(path) {
		if rel, err := filepath.Rel(root, path); err == nil {
			query = filepath.ToSlash(rel)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(codemap.FormatImpact(idx.Impact(query, codemap.MaxDepthAll, limit), limit), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
}

func writeImpactUnavailable(b *strings.Builder, err error) {
	if errors.Is(err, lspci.ErrUnsupportedCapability) {
		fmt.Fprintf(b, "  unavailable: unsupported capability (%v)\n", err)
		return
	}
	fmt.Fprintf(b, "  unavailable: %v\n", err)
}

func truncateImpactText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	if cut == 0 {
		return "…"
	}
	return s[:cut] + "…"
}
