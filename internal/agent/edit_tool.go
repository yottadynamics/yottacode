package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EditFileTool performs a surgical replacement inside an existing file.
// Strictly better than write_file for code edits: it preserves the rest of the
// file and refuses to apply a non-unique match unless replace_all is set,
// which catches stale assumptions before they corrupt code.
type EditFileTool struct {
	Cwd       string
	WriteOpts WritePathOptions
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
		return "", fmt.Errorf("edit_file: old_string and new_string are identical — no-op")
	}
	p := resolvePath(t.Cwd, a.Path)
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
		return "", fmt.Errorf("edit_file: old_string not found in %s", p)
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
	return fmt.Sprintf("edited %s: %d replacement(s)", p, n), nil
}
