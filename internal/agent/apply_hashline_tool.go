package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	return "Apply hash-anchored text edits to one file. Provide path and hunks with offset, length, hash, old, and new. Copy offset, length and hash from a `# hashline` receipt (read_file or read_many_files with anchors=true) and set old to the exact text of that span; length must be old's byte length, and old must end with a newline exactly when the receipt says ends_with_newline=true. A syntax_range receipt carries old verbatim; copy it as is. new is the replacement, possibly empty. To insert, anchor on adjacent text: set old to a neighbouring line and new to that line plus the addition (an empty old is rejected). In files that use CRLF line endings, old and new may use plain newlines; they are matched to the file. Mutating application requires approval, validates the write path, and rejects stale or ambiguous anchors with a re-read range instead of guessing."
}

func (t *ApplyHashlineTool) Schema() map[string]any {
	anchorProps := map[string]any{
		"offset": map[string]any{"type": "integer", "description": "Byte offset from the receipt"},
		"length": map[string]any{"type": "integer", "description": "Byte length from the receipt; must equal the byte length of old"},
		"hash":   map[string]any{"type": "string", "description": "16 lowercase hex characters copied from the receipt; do not compute it"},
		"old":    map[string]any{"type": "string", "description": "Exact text of the receipt's span, including the final newline when ends_with_newline=true; non-empty"},
		"new":    map[string]any{"type": "string", "description": "Replacement text, possibly empty"},
		"op":     map[string]any{"type": "string", "description": "Optional documentation label such as replace/delete; an insert is a replace of adjacent text. Validation is determined by old/new/length"},
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
	oldBytes, err := hashline.ReadFileForEdit(p)
	if err != nil {
		return "", formatHashlineApplyError(err)
	}
	out, err := hashline.Apply(oldBytes, alignHunkEOL(oldBytes, parsed))
	if err != nil {
		return "", formatHashlineApplyError(err)
	}
	if string(oldBytes) == string(out) {
		return "", fmt.Errorf("apply_hashline: no changes produced")
	}
	if err := hashline.ReplaceFileIfUnchanged(p, oldBytes, out); err != nil {
		return "", formatHashlineApplyError(err)
	}
	msg := fmt.Sprintf("applied %d hashline hunk(s) to %s\n", len(parsed), p)
	msg += boundedUnifiedDiff(p, string(oldBytes), string(out), 2, 80)
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, p, string(out)); note != "" {
		msg += note
	}
	return msg, nil
}

// alignHunkEOL reconciles line endings between what the model sent and a CRLF
// file. A model reading CRLF text emits plain newlines (it cannot reliably
// reproduce invisible carriage returns), while the receipt hashes the real
// bytes. The hashline library stays byte-exact; this adapts the hunks first:
//
//   - old is used exactly as sent when it already matches its hash. Only when
//     it does not, and its CRLF form does, is the CRLF form used, so a genuine
//     mismatch is never masked.
//   - new is converted to CRLF when old was CRLF-only or had no newline at all,
//     so an edit cannot leave the file with mixed endings. If old itself was
//     mixed, the model is managing endings and new is left untouched.
//
// LF files are never touched: converting toward CRLF only, and only in files
// that are predominantly CRLF, avoids guessing at intent elsewhere.
func alignHunkEOL(src []byte, hunks []hashline.Hunk) []hashline.Hunk {
	crlf := bytes.Count(src, []byte("\r\n"))
	if crlf <= bytes.Count(src, []byte("\n"))-crlf {
		return hunks
	}
	out := make([]hashline.Hunk, len(hunks))
	for i, h := range hunks {
		out[i] = h
		converted := false
		if spanHash(h.Old) != h.Anchor.Hash {
			if cand := toCRLF(h.Old); spanHash(cand) == h.Anchor.Hash {
				out[i].Old, converted = cand, true
			}
		}
		if converted || !hasBareLF(out[i].Old) {
			out[i].New = toCRLF(h.New)
		}
	}
	return out
}

func spanHash(b []byte) string {
	a, err := hashline.HashSpan(b, 0, len(b))
	if err != nil {
		return ""
	}
	return a.Hash
}

// toCRLF converts every line ending to CRLF; it is idempotent.
func toCRLF(b []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

// hasBareLF reports whether b contains a newline not preceded by a carriage return.
func hasBareLF(b []byte) bool {
	for i, c := range b {
		if c == '\n' && (i == 0 || b[i-1] != '\r') {
			return true
		}
	}
	return false
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
	switch applyErr.Kind {
	case hashline.ErrStaleAnchor, hashline.ErrAmbiguousAnchor:
		return fmt.Errorf("apply_hashline: %w; call read_file for the suggested range, copy the current text and hashline receipt, then retry", applyErr)
	case hashline.ErrHashMismatch:
		return fmt.Errorf("apply_hashline: %w; call read_file(anchors=true) fresh and use the old text and hash from that same read", applyErr)
	case hashline.ErrInvalidHash:
		return fmt.Errorf("apply_hashline: %w; use exactly %d lowercase hexadecimal characters from a fresh hashline receipt", applyErr, hashline.HashHexLength)
	case hashline.ErrConcurrentWrite:
		return fmt.Errorf("apply_hashline: %w; the file changed while the edit was being prepared — re-read it and retry", applyErr)
	case hashline.ErrFileTooLarge:
		return fmt.Errorf("apply_hashline: %w; use run_bash with sed/awk for a targeted change, or split it into a smaller edit", applyErr)
	}
	return fmt.Errorf("apply_hashline: %w", applyErr)
}
