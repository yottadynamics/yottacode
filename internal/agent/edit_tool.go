package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// EditFileTool performs a surgical replacement inside an existing file.
// Strictly better than write_file for code edits: it preserves the rest of the
// file and refuses to apply a non-unique match unless replace_all is set,
// which catches stale assumptions before they corrupt code.
type EditFileTool struct {
	Cwd        *CwdRef
	WriteOpts  WritePathOptions
	LSPManager *lspci.Manager
	LSPServers map[string][]string
}

func (t *EditFileTool) Name() string { return "edit_file" }

func (t *EditFileTool) Description() string {
	return "Replace exactly one occurrence of old_string with new_string in the given file. " +
		"Set replace_all=true to replace every occurrence. " +
		"Fails if old_string is not present, or if it appears more than once and replace_all is false."
}

func (t *EditFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "File to edit (absolute or cwd-relative)"},
			"old_string":  map[string]any{"type": "string", "description": "Text that currently exists in the file"},
			"new_string":  map[string]any{"type": "string", "description": "Replacement text (may be empty to delete)"},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence (default: false)"},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

func (t *EditFileTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports the target file so /checkpoints can restore
// the pre-edit contents on rewind.
func (t *EditFileTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a editArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Path == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Path)}
}

func (t *EditFileTool) PreviewCall(argsJSON string) string {
	var a editArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	mode := "single"
	if a.ReplaceAll {
		mode = "all"
	}
	return fmt.Sprintf("edit_file(%s, %s)", a.Path, mode)
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *EditFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a editArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("edit_file: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("edit_file: path is required")
	}
	if a.OldString == "" {
		return "", fmt.Errorf("edit_file: old_string must not be empty (would match everywhere)")
	}
	if a.OldString == a.NewString {
		return "", fmt.Errorf("edit_file: old_string and new_string are identical — no change to make. If you meant to edit the file, re-read it and provide a new_string that differs from old_string (for example, the corrected line)")
	}
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("edit_file: %w", err)
	}
	contents, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("edit_file: %w", err)
	}
	src := string(contents)
	count := strings.Count(src, a.OldString)
	if count == 0 {
		// Exact match failed. The dominant cause is a model-reconstructed
		// old_string that drifted from the file by trailing whitespace or
		// CRLF vs LF (prose docs like YOTTACODE.md are the worst case).
		// Retry with a whitespace-tolerant, full-line-aligned match — but
		// only for the single-replacement path, and only when it is unique,
		// so we never silently apply an ambiguous or intra-line guess.
		if !a.ReplaceAll {
			if start, end, mcount := editTolerantLineMatch(src, a.OldString); mcount == 1 {
				out := src[:start] + a.NewString + src[end:]
				if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
					return "", fmt.Errorf("edit_file: write: %w", err)
				}
				return t.editResult(ctx, p, out, "edited %s: 1 replacement (matched ignoring trailing whitespace / line endings)")
			} else if mcount > 1 {
				return "", fmt.Errorf("edit_file: old_string is not present exactly, and a whitespace-insensitive match is ambiguous (%d candidates) in %s — re-read the file and copy the exact text, or add surrounding lines to old_string for a unique match", mcount, p)
			}
		}
		return "", editNotFoundErr(p, src, a.OldString)
	}
	if count > 1 && !a.ReplaceAll {
		return "", fmt.Errorf("edit_file: old_string appears %d times in %s — set replace_all=true to apply, or refine old_string for a unique match", count, p)
	}
	var out string
	var n int
	if a.ReplaceAll {
		out = strings.ReplaceAll(src, a.OldString, a.NewString)
		n = count
	} else {
		out = strings.Replace(src, a.OldString, a.NewString, 1)
		n = 1
	}
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		return "", fmt.Errorf("edit_file: write: %w", err)
	}
	return t.editResult(ctx, p, out, fmt.Sprintf("edited %%s: %d replacement(s)", n))
}

func (t *EditFileTool) editResult(ctx context.Context, path, text, format string) (string, error) {
	msg := fmt.Sprintf(format, path)
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, path, text); note != "" {
		msg += "\n" + note
	}
	return msg, nil
}

// editNormalizeMatchLine strips a trailing CR and trailing spaces/tabs so
// the tolerant matcher ignores exactly the drift that makes a
// model-reconstructed old_string miss: CRLF vs LF and per-line trailing
// whitespace. Leading indentation is preserved — it is semantically
// meaningful in code, and loosening it risks matching the wrong block.
func editNormalizeMatchLine(s string) string {
	return strings.TrimRight(strings.TrimRight(s, "\r"), " \t")
}

// editTolerantLineMatch looks for contiguous blocks of whole source lines
// that equal old line-for-line under editNormalizeMatchLine. It returns the
// byte span [start,end) of the first such block (internal newlines included,
// the block's own trailing newline excluded) and the total number of blocks
// found (0, 1, or more). Because it only aligns to full source lines, it can
// never mangle an intra-line target — those still require an exact match. It
// declines old_strings that end in a newline, whose trailing-blank-line
// splice is ambiguous; those fall through to the improved not-found error.
func editTolerantLineMatch(src, old string) (start, end, count int) {
	if old == "" || strings.HasSuffix(old, "\n") {
		return 0, 0, 0
	}
	srcLines := strings.Split(src, "\n")
	oldLines := strings.Split(old, "\n")
	n := len(oldLines)
	if n == 0 || len(srcLines) < n {
		return 0, 0, 0
	}
	lineStart := make([]int, len(srcLines)+1)
	for i, l := range srcLines {
		lineStart[i+1] = lineStart[i] + len(l) + 1 // +1 for the '\n'
	}
	normOld := make([]string, n)
	for j, l := range oldLines {
		normOld[j] = editNormalizeMatchLine(l)
	}
	for i := 0; i+n <= len(srcLines); i++ {
		matched := true
		for j := 0; j < n; j++ {
			if editNormalizeMatchLine(srcLines[i+j]) != normOld[j] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		count++
		if count == 1 {
			start = lineStart[i]
			end = lineStart[i+n-1] + len(srcLines[i+n-1])
		}
	}
	if count != 1 {
		return 0, 0, count
	}
	return start, end, 1
}

// editNotFoundErr builds the "old_string not found" error, pointing the
// model at the closest actual line so it can self-correct on the next turn
// instead of retrying the same wrong text.
func editNotFoundErr(path, src, old string) error {
	if line, num := editClosestLine(src, old); line != "" {
		return fmt.Errorf("edit_file: old_string not found in %s — the closest line is %d: %q. Re-read the file and copy its exact current text (including whitespace) into old_string", path, num, line)
	}
	return fmt.Errorf("edit_file: old_string not found in %s — re-read the file with read_file and copy its exact current text into old_string", path)
}

// editClosestLine returns the source line most similar to the first non-blank
// line of old (scored by common prefix+suffix length after normalization),
// with its 1-based line number. Returns "" when nothing is meaningfully
// close, so the caller can fall back to a generic hint.
func editClosestLine(src, old string) (string, int) {
	var target string
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			target = editNormalizeMatchLine(l)
			break
		}
	}
	if target == "" {
		return "", 0
	}
	srcLines := strings.Split(src, "\n")
	bestScore, bestIdx := 0, -1
	for i, l := range srcLines {
		if score := editCommonAffixLen(editNormalizeMatchLine(l), target); score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	minScore := 4
	if third := len(target) / 3; third > minScore {
		minScore = third
	}
	if bestIdx < 0 || bestScore < minScore {
		return "", 0
	}
	line := strings.TrimRight(srcLines[bestIdx], "\r")
	if len(line) > 120 {
		line = strings.ToValidUTF8(line[:120], "") + "…"
	}
	return line, bestIdx + 1
}

// editCommonAffixLen scores similarity as the combined length of the common
// prefix and the common suffix of a and b (the suffix search starts past the
// shared prefix so the same characters can't be counted from both ends).
func editCommonAffixLen(a, b string) int {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return p + s
}
