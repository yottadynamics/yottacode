package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
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
				"type":        "array",
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
	// Relocated names any anchors this op resolved by relocation ("N→M"), so
	// the result can tell the caller the edit landed at a shifted line.
	Relocated []string
}

func (t *EditAnchoredTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var preview anchoredEditArgs
	if err := json.Unmarshal([]byte(argsJSON), &preview); err != nil {
		return "", fmt.Errorf("edit_anchored: invalid args: %w", err)
	}
	cwd := t.Cwd.Get()
	p := resolvePath(cwd, preview.Path)
	return withEditPathLock(p, func() (string, error) {
		return t.executeUnlocked(ctx, argsJSON, p)
	})
}

func (t *EditAnchoredTool) executeUnlocked(ctx context.Context, argsJSON string, resolvedPath string) (string, error) {
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
	p := resolvedPath
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	contents, err := hashline.ReadFileForEdit(p)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	src := string(contents)
	lineTexts, spans := splitLinesWithSpans(contents)
	lines := make([]string, len(lineTexts))
	for i, line := range lineTexts {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	idx := buildAnchoredLineIndex(lines)
	resolved, err := resolveAnchoredOps(idx, a.Operations)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	outBytes, err := applyAnchoredByteOps(contents, lines, spans, resolved)
	if err != nil {
		return "", fmt.Errorf("edit_anchored: %w", err)
	}
	out := string(outBytes)
	if out == src {
		return "", fmt.Errorf("edit_anchored: edit is a no-op")
	}
	if err := hashline.ReplaceFileIfUnchanged(p, contents, outBytes); err != nil {
		return "", fmt.Errorf("edit_anchored: write: %w", err)
	}
	msg := fmt.Sprintf("edited %s: %d anchored operation(s)", p, len(resolved))
	var moved []string
	for _, r := range resolved {
		moved = append(moved, r.Relocated...)
	}
	if len(moved) > 0 {
		msg += fmt.Sprintf("\nnote: stale anchor(s) relocated by matching their text (original line → current line): %s. Re-read before further edits to this file", strings.Join(moved, ", "))
	}
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, p, out); note != "" {
		msg += "\n" + note
	}
	return msg, nil
}

// relocationNotes lists "from→to" for each resolved line that was relocated.
func relocationNotes(lines ...anchoredLine) []string {
	var out []string
	for _, l := range lines {
		if l.RelocatedFrom != 0 {
			out = append(out, fmt.Sprintf("%d→%d", l.RelocatedFrom, l.LineNumber))
		}
	}
	return out
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
			resolved = append(resolved, anchoredResolvedOp{Kind: op.Op, StartLine: startLine.LineNumber, EndLine: endLine.LineNumber, NewText: op.NewText, Order: i, Relocated: relocationNotes(startLine, endLine)})
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
			resolved = append(resolved, anchoredResolvedOp{Kind: op.Op, InsertAt: line.LineNumber, After: op.Op == "insert_after", NewText: op.NewText, Order: i, Relocated: relocationNotes(line)})
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

// applyAnchoredByteOps applies resolved operations against the original byte
// spans. Unlike the line-slice implementation, this keeps every untouched
// terminator (including mixed line endings and a missing final terminator).
func applyAnchoredByteOps(src []byte, _ []string, spans []lineSpan, ops []anchoredResolvedOp) ([]byte, error) {
	out := append([]byte(nil), src...)
	sort.SliceStable(ops, func(i, j int) bool {
		key := func(op anchoredResolvedOp) int {
			if op.Kind == "insert_before" || op.Kind == "insert_after" {
				return op.InsertAt
			}
			return op.StartLine
		}
		ki, kj := key(ops[i]), key(ops[j])
		if ki != kj {
			return ki > kj
		}
		return ops[i].Order > ops[j].Order
	})
	for _, op := range ops {
		start, end := 0, 0
		styleAt := 0
		switch op.Kind {
		case "replace_range", "delete_range":
			start = spans[op.StartLine-1].offset
			last := spans[op.EndLine-1]
			end = last.offset + last.length
			styleAt = op.StartLine - 1
		case "insert_before", "insert_after":
			styleAt = op.InsertAt - 1
			if op.Kind == "insert_before" {
				start = spans[styleAt].offset
			} else {
				start = spans[styleAt].offset + spans[styleAt].length
			}
			end = start
		default:
			return nil, fmt.Errorf("unsupported operation %q", op.Kind)
		}
		replacement := ""
		if op.Kind == "replace_range" || op.Kind == "insert_before" || op.Kind == "insert_after" {
			replacement = preserveEditNewlines(op.NewText, lineEndingAt(src, spans, styleAt))
			if (op.Kind == "insert_before" || op.Kind == "insert_after") && !strings.HasSuffix(replacement, "\n") {
				if ending := lineEndingAt(src, spans, styleAt); ending != "" && (op.Kind == "insert_before" || spans[styleAt].offset+spans[styleAt].length < len(src)) {
					replacement += ending
				}
			}
			if op.Kind == "replace_range" {
				last := spans[op.EndLine-1]
				original := src[last.offset : last.offset+last.length]
				if !strings.HasSuffix(replacement, "\n") {
					if bytes.HasSuffix(original, []byte("\r\n")) {
						replacement += "\r\n"
					} else if bytes.HasSuffix(original, []byte("\n")) {
						replacement += "\n"
					}
				}
				if strings.TrimSuffix(string(out[start:end]), "\r\n") == replacement || strings.TrimSuffix(string(out[start:end]), "\n") == replacement {
					return nil, fmt.Errorf("operation %d/%d: replace_range at lines %d-%d is a no-op; omit it or provide different replacement text", op.Order+1, len(ops), op.StartLine, op.EndLine)
				}
			}
		}
		out = append(out[:start], append([]byte(replacement), out[end:]...)...)
	}
	return out, nil
}

func lineEndingAt(src []byte, spans []lineSpan, line int) string {
	if line >= 0 && line < len(spans) {
		b := src[spans[line].offset : spans[line].offset+spans[line].length]
		if bytes.HasSuffix(b, []byte("\r\n")) {
			return "\r\n"
		}
	}
	for _, span := range spans {
		b := src[span.offset : span.offset+span.length]
		if bytes.HasSuffix(b, []byte("\r\n")) {
			return "\r\n"
		}
		if bytes.HasSuffix(b, []byte("\n")) {
			return "\n"
		}
	}
	return "\n"
}

func preserveEditNewlines(text, ending string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\n", ending)
}
