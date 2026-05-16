package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxReadBytes is the hard cap on how much of the file we read into
	// memory before slicing into lines. With the default 2000-line
	// window and ~80-char lines this leaves comfortable headroom; it
	// also bounds the worst-case cost of a single pathologically long
	// line.
	maxReadBytes = 512 * 1024 // 512 KiB

	// defaultReadLines mirrors Claude Code's Read tool: 2000 lines is
	// the implicit limit when the model omits an explicit `limit`.
	defaultReadLines = 2000
)

// ReadFileTool lets the model fetch local file contents. Read-only, no
// approval. DenyReadPaths blocks a small set of credential-bearing
// locations (see DefaultDenyReadPaths) so prompt injection can't
// silently exfiltrate keys; everything else is fair game.
type ReadFileTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read a UTF-8 text file. Output is `cat -n` style: every line is prefixed with its 1-indexed line number and a tab, " +
		"so you can cite `file:line` directly and feed exact text to edit_file. " +
		"Optional offset (1-indexed start line, default 1) and limit (lines, default 2000) read a specific range — use these instead of `sed -n 'A,Bp' file` via run_bash. " +
		"When more content follows the returned window, a trailing '…[truncated]' marker is appended."
}

func (t *ReadFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or cwd-relative path to the file",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "1-indexed line number to start reading from (default 1). Values < 1 are clamped to 1.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum number of lines to return (default %d).", defaultReadLines),
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) RequiresApproval(string) bool { return false }
func (t *ReadFileTool) ParallelSafe(string) bool     { return true }

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"` // 1-indexed start line
	Limit  int    `json:"limit"`  // max lines to return
}

func (t *ReadFileTool) PreviewCall(argsJSON string) string {
	var a readFileArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Offset == 0 && a.Limit == 0 {
		return fmt.Sprintf("read_file(%s)", a.Path)
	}
	return fmt.Sprintf("read_file(%s, offset=%d, limit=%d)", a.Path, a.Offset, a.Limit)
}

func (t *ReadFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a readFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("read_file: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("read_file: path is required")
	}
	startLine := a.Offset
	if startLine < 1 {
		startLine = 1
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultReadLines
	}

	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()

	// Read up to maxReadBytes+1 so we can detect "file overflows our
	// in-memory cap" vs "exact EOF".
	buf := make([]byte, maxReadBytes+1)
	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.EOF):
		// empty file — nothing to return.
		return "", nil
	case errors.Is(err, io.ErrUnexpectedEOF):
		// fewer bytes than buf — normal for short files.
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	fileExceedsCap := n > maxReadBytes
	if fileExceedsCap {
		// Trim back to the last complete line so we don't emit a half line.
		if last := strings.LastIndexByte(string(buf[:maxReadBytes]), '\n'); last >= 0 {
			n = last + 1
		} else {
			n = maxReadBytes
		}
	}

	lines := strings.Split(string(buf[:n]), "\n")
	// A file ending in '\n' produces an empty trailing element; drop it.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if startLine > total {
		// Offset past EOF — empty result, no error.
		return "", nil
	}

	startIdx := startLine - 1
	endIdx := startIdx + limit
	truncated := fileExceedsCap || endIdx < total
	if endIdx > total {
		endIdx = total
	}
	selected := lines[startIdx:endIdx]

	var sb strings.Builder
	for i, line := range selected {
		fmt.Fprintf(&sb, "%6d\t%s", startLine+i, line)
		if i < len(selected)-1 || truncated {
			sb.WriteByte('\n')
		}
	}
	if truncated {
		sb.WriteString("…[truncated]")
	}
	return sb.String(), nil
}

// WriteFileTool creates or overwrites a file. Always needs approval; the
// WriteOpts validator pre-rejects out-of-cwd, symlinked, or deny-listed
// paths *before* the approval modal opens, so the model can't trick a
// distracted user into approving a misleading path.
type WriteFileTool struct {
	Cwd       *CwdRef
	WriteOpts WritePathOptions
}

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Description() string {
	return "Create or overwrite a text file at the given path. Directories are created if needed. Returns a short confirmation on success."
}

func (t *WriteFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "Destination path"},
			"content": map[string]any{"type": "string", "description": "File contents"},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports the destination path so /checkpoints can
// restore the pre-write contents (or remove the file if it didn't
// exist before this turn).
func (t *WriteFileTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Path == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Path)}
}

func (t *WriteFileTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("write_file(%s, %d bytes)", a.Path, len(a.Content))
}

func (t *WriteFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("write_file: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("write_file: path is required")
	}
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("write_file: mkdir: %w", err)
	}
	if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), p), nil
}
