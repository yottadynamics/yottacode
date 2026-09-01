package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// ApplyHashlineTool applies file edits only after validating a content hash
// anchor captured from a previous read. It is stricter than apply_diff: stale
// anchors return recoverable errors instead of falling back to fuzzy patching.
type ApplyHashlineTool struct {
	Cwd        *CwdRef
	WriteOpts  WritePathOptions
	LSPManager *lspci.Manager
	LSPServers map[string][]string
}

func (t *ApplyHashlineTool) Name() string { return "apply_hashline" }

func (t *ApplyHashlineTool) Description() string {
	return "Apply hash-anchored text edits to one file. Provide path and hunks with offset, length, hash, old, and new; hash is sha256 of the exact old span truncated to 16 hex characters. Mutating application requires approval, validates the write path, and rejects stale or ambiguous anchors with a re-read range instead of guessing."
}

func (t *ApplyHashlineTool) Schema() map[string]any {
	anchorProps := map[string]any{
		"offset": map[string]any{"type": "integer", "description": "Byte offset recorded when the span was read"},
		"length": map[string]any{"type": "integer", "description": "Byte length of the old span"},
		"hash":   map[string]any{"type": "string", "description": "sha256(old span) truncated to 16 lowercase hex characters"},
		"old":    map[string]any{"type": "string", "description": "Exact old text that must hash to hash"},
		"new":    map[string]any{"type": "string", "description": "Replacement text, possibly empty"},
		"op":     map[string]any{"type": "string", "description": "Optional documentation label such as replace/insert/delete; validation is determined by old/new/length"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "File to edit (absolute or cwd-relative)"},
			"offset": anchorProps["offset"],
			"length": anchorProps["length"],
			"hash":   anchorProps["hash"],
			"old":    anchorProps["old"],
			"new":    anchorProps["new"],
			"op":     anchorProps["op"],
			"hunks": map[string]any{
				"type":        "array",
				"description": "Multiple hashline hunks. If omitted, the top-level offset/length/hash/old/new fields form one hunk.",
				"items": map[string]any{
					"type":       "object",
					"properties": anchorProps,
					"required":   []string{"offset", "length", "hash", "old", "new"},
				},
			},
		},
		"required": []string{"path"},
	}
}

func (t *ApplyHashlineTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports the target file so checkpoints can restore the
// pre-edit content if the approved patch needs to be rewound.
func (t *ApplyHashlineTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a applyHashlineArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Path == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Path)}
}

func (t *ApplyHashlineTool) PreviewCall(argsJSON string) string {
	a, _ := parseApplyHashlineArgs(argsJSON)
	count := len(a.Hunks)
	if count == 0 && a.Hash != "" {
		count = 1
	}
	return fmt.Sprintf("apply_hashline(%s, %d hunks)", a.Path, count)
}

func (t *ApplyHashlineTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	a, err := parseApplyHashlineArgs(argsJSON)
	if err != nil {
		return "", fmt.Errorf("apply_hashline: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("apply_hashline: path is required")
	}
	parsed, err := a.toHashlineHunks()
	if err != nil {
		return "", fmt.Errorf("apply_hashline: %w", err)
	}
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("apply_hashline: %w", err)
	}
	oldBytes, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("apply_hashline: %w", err)
	}
	out, err := hashline.Apply(oldBytes, parsed)
	if err != nil {
		return "", formatHashlineApplyError(err)
	}
	if string(oldBytes) == string(out) {
		return "", fmt.Errorf("apply_hashline: no changes produced")
	}
	if err := hashline.ApplyFile(p, parsed); err != nil {
		return "", formatHashlineApplyError(err)
	}
	msg := fmt.Sprintf("applied %d hashline hunk(s) to %s\n", len(parsed), p)
	msg += boundedUnifiedDiff(p, string(oldBytes), string(out), 2, 80)
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, p, string(out)); note != "" {
		msg += note
	}
	return msg, nil
}

type applyHashlineArgs struct {
	Path   string              `json:"path"`
	Offset int                 `json:"offset"`
	Length int                 `json:"length"`
	Hash   string              `json:"hash"`
	Old    string              `json:"old"`
	New    string              `json:"new"`
	Op     string              `json:"op"`
	Hunks  []applyHashlineHunk `json:"hunks"`
}

type applyHashlineHunk struct {
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	Hash   string `json:"hash"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Op     string `json:"op"`
}

func parseApplyHashlineArgs(argsJSON string) (applyHashlineArgs, error) {
	var a applyHashlineArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return applyHashlineArgs{}, err
	}
	return a, nil
}

func (a applyHashlineArgs) toHashlineHunks() ([]hashline.Hunk, error) {
	raw := a.Hunks
	if len(raw) == 0 {
		if a.Hash == "" {
			return nil, fmt.Errorf("either hunks or top-level offset/length/hash/old/new is required")
		}
		raw = []applyHashlineHunk{{Offset: a.Offset, Length: a.Length, Hash: a.Hash, Old: a.Old, New: a.New, Op: a.Op}}
	}
	out := make([]hashline.Hunk, 0, len(raw))
	for i, h := range raw {
		if h.Hash == "" {
			return nil, fmt.Errorf("hunk %d hash is required", i)
		}
		if h.Op != "" {
			switch h.Op {
			case "replace", "insert", "delete":
			default:
				return nil, fmt.Errorf("hunk %d op must be replace, insert, or delete", i)
			}
		}
		out = append(out, hashline.Hunk{
			Anchor: hashline.Anchor{Path: a.Path, Offset: h.Offset, Length: h.Length, Hash: h.Hash},
			Old:    []byte(h.Old),
			New:    []byte(h.New),
		})
	}
	return out, nil
}

func formatHashlineApplyError(err error) error {
	var applyErr *hashline.ApplyError
	if !errors.As(err, &applyErr) {
		return fmt.Errorf("apply_hashline: %w", err)
	}
	msg := "apply_hashline: " + applyErr.Error()
	if applyErr.Kind == hashline.ErrStaleAnchor || applyErr.Kind == hashline.ErrAmbiguousAnchor {
		msg += "; call read_file for the suggested range, copy the current text and hashline receipt, then retry"
	}
	return errors.New(msg)
}
