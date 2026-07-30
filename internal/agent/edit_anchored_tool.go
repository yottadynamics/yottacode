package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

type EditAnchoredTool struct {
	Cwd        *CwdRef
	WriteOpts  WritePathOptions
	LSPManager *lspci.Manager
	LSPServers map[string][]string
}

func (t *EditAnchoredTool) Name() string { return "edit_anchored" }
func (t *EditAnchoredTool) Description() string {
	return "Apply anchor-validated line edits to a file using start/end anchors from an anchored read. Supports replace_range, delete_range, insert_before, and insert_after."
}
func (t *EditAnchoredTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "File to edit (absolute or cwd-relative)"},
			"operations": map[string]any{
				"type": "array",
				"description": "Ordered anchor-based edit operations",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"op":           map[string]any{"type": "string", "description": "One of replace_range, delete_range, insert_before, insert_after"},
						"anchor":       map[string]any{"type": "string", "description": "Single anchor for insert_before/insert_after"},
						"start_anchor": map[string]any{"type": "string", "description": "Start anchor for replace_range/delete_range"},
						"end_anchor":   map[string]any{"type": "string", "description": "End anchor for replace_range/delete_range"},
						"new_text":     map[string]any{"type": "string", "description": "Inserted or replacement text"},
					},
					"required": []string{"op"},
				},
			},
		},
		"required": []string{"path", "operations"},
	}
}
func (t *EditAnchoredTool) RequiresApproval(string) bool { return true }

func (t *EditAnchoredTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Path == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Path)}
}

func (t *EditAnchoredTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path       string            `json:"path"`
		Operations []json.RawMessage `json:"operations"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("edit_anchored(%s, %d ops)", a.Path, len(a.Operations))
}

type anchoredEditArgs struct {
	Path       string                `json:"path"`
	Operations []anchoredEditRequest `json:"operations"`
}

type anchoredEditRequest struct {
	Op          string `json:"op"`
	Anchor      string `json:"anchor"`
	StartAnchor string `json:"start_anchor"`
	EndAnchor   string `json:"end_anchor"`
	NewText     string `json:"new_text"`
}

type anchoredResolvedOp struct {
	Kind      string
	StartLine int
	EndLine   int
	InsertAt  int
	After     bool
	NewText   string
	Order     int
	Summary   string
}

func (t *EditAnchoredTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a anchoredEditArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("edit_anchored: invalid args: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("edit_anchored: path is required")
	}
	if len(a.Operations) == 0 {
		return "", fmt.Errorf("edit_anchored: operations is required")
	}
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	contents, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	src := string(contents)
	hadTrailingNewline := strings.HasSuffix(src, "\n")
	lines := strings.Split(src, "\n")
	if hadTrailingNewline && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	idx := buildAnchoredLineIndex(lines)
	resolved, err := resolveAnchoredOps(idx, a.Operations)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	outLines, err := applyAnchoredOps(lines, resolved)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	out := strings.Join(outLines, "\n")
	if hadTrailingNewline {
		out += "\n"
	}
	if out == src {
		return "", fmt.Errorf("edit_anchored: edit is a no-op")
	}
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		return "", fmt.Errorf("edit_anchored: write: %w", err)
	}
	msg := fmt.Sprintf("edited %s: %d anchored operation(s)", p, len(resolved))
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, p, out); note != "" {
		msg += "\n" + note
	}
	return msg, nil
}

func resolveAnchoredOps(idx anchoredLineIndex, ops []anchoredEditRequest) ([]anchoredResolvedOp, error) {
	resolved := make([]anchoredResolvedOp, 0, len(ops))
	for i, op := range ops {
		switch op.Op {
		case "replace_range", "delete_range":
			startRef, err := parseAnchoredRef(op.StartAnchor)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			endRef, err := parseAnchoredRef(op.EndAnchor)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			startLine, err := resolveAnchoredRef(idx, startRef)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			endLine, err := resolveAnchoredRef(idx, endRef)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			if startLine.LineNumber > endLine.LineNumber {
				return nil, fmt.Errorf("operation %d: start_anchor must be on or before end_anchor", i+1)
			}
			if op.Op == "replace_range" && op.NewText == "" {
				return nil, fmt.Errorf("operation %d: replace_range requires non-empty new_text", i+1)
			}
			if op.Op == "delete_range" && strings.TrimSpace(op.NewText) != "" {
				return nil, fmt.Errorf("operation %d: delete_range does not accept new_text", i+1)
			}
			resolved = append(resolved, anchoredResolvedOp{Kind: op.Op, StartLine: startLine.LineNumber, EndLine: endLine.LineNumber, NewText: op.NewText, Order: i})
		case "insert_before", "insert_after":
			if op.NewText == "" {
				return nil, fmt.Errorf("operation %d: %s requires non-empty new_text", i+1, op.Op)
			}
			ref, err := parseAnchoredRef(op.Anchor)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			line, err := resolveAnchoredRef(idx, ref)
			if err != nil {
				return nil, fmt.Errorf("operation %d: %w", i+1, err)
			}
			resolved = append(resolved, anchoredResolvedOp{Kind: op.Op, InsertAt: line.LineNumber, After: op.Op == "insert_after", NewText: op.NewText, Order: i})
		default:
			return nil, fmt.Errorf("operation %d: unsupported op %q", i+1, op.Op)
		}
	}
	for i := 0; i < len(resolved); i++ {
		for j := i + 1; j < len(resolved); j++ {
			if spansOverlap(resolved[i], resolved[j]) {
				return nil, fmt.Errorf("operations %d and %d overlap", resolved[i].Order+1, resolved[j].Order+1)
			}
			if sameInsertPoint(resolved[i], resolved[j]) {
				return nil, fmt.Errorf("operations %d and %d target the same insertion point", resolved[i].Order+1, resolved[j].Order+1)
			}
		}
	}
	return resolved, nil
}

func spansOverlap(a, b anchoredResolvedOp) bool {
	if a.Kind == "insert_before" || a.Kind == "insert_after" || b.Kind == "insert_before" || b.Kind == "insert_after" {
		return false
	}
	return a.StartLine <= b.EndLine && b.StartLine <= a.EndLine
}

func sameInsertPoint(a, b anchoredResolvedOp) bool {
	if (a.Kind != "insert_before" && a.Kind != "insert_after") || (b.Kind != "insert_before" && b.Kind != "insert_after") {
		return false
	}
	return a.InsertAt == b.InsertAt && a.After == b.After
}

func applyAnchoredOps(lines []string, ops []anchoredResolvedOp) ([]string, error) {
	out := append([]string(nil), lines...)
	sort.SliceStable(ops, func(i, j int) bool {
		ai, aj := ops[i], ops[j]
		keyI := ai.InsertAt
		if ai.Kind != "insert_before" && ai.Kind != "insert_after" {
			keyI = ai.StartLine
		}
		keyJ := aj.InsertAt
		if aj.Kind != "insert_before" && aj.Kind != "insert_after" {
			keyJ = aj.StartLine
		}
		if keyI != keyJ {
			return keyI > keyJ
		}
		return ai.Order > aj.Order
	})
	for _, op := range ops {
		switch op.Kind {
		case "replace_range", "delete_range":
			startIdx := op.StartLine - 1
			endIdx := op.EndLine
			replacement := []string{}
			if op.Kind == "replace_range" {
				replacement = strings.Split(op.NewText, "\n")
				if len(replacement) == 1 && replacement[0] == "" {
					return nil, fmt.Errorf("replace_range at lines %d-%d is a no-op", op.StartLine, op.EndLine)
				}
			}
			if op.Kind == "replace_range" && strings.Join(out[startIdx:endIdx], "\n") == op.NewText {
				return nil, fmt.Errorf("replace_range at lines %d-%d is a no-op", op.StartLine, op.EndLine)
			}
			out = append(append(out[:startIdx], replacement...), out[endIdx:]...)
		case "insert_before", "insert_after":
			insert := strings.Split(op.NewText, "\n")
			insertAt := op.InsertAt - 1
			if op.After {
				insertAt = op.InsertAt
			}
			out = append(append(out[:insertAt], insert...), out[insertAt:]...)
		default:
			return nil, fmt.Errorf("unsupported operation %q", op.Kind)
		}
	}
	return out, nil
}
