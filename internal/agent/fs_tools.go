package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxReadBytes = 512 * 1024 // 512 KiB read cap — tool responses stay small

// ReadFileTool lets the model fetch local file contents. Read-only, no
// approval. DenyReadPaths blocks a small set of credential-bearing
// locations (see DefaultDenyReadPaths) so prompt injection can't
// silently exfiltrate keys; everything else is fair game.
type ReadFileTool struct {
	Cwd           string
	DenyReadPaths []string
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read a UTF-8 text file from disk. Optional offset (in bytes, default 0) " +
		"and limit (in bytes, default 512 KiB cap) let you page through large files. " +
		"If the read window stops before EOF, the response includes a '[truncated]' marker."
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
				"description": "Byte offset to start reading from (default 0). Negative values are clamped to 0.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Max bytes to return (default %d). Capped at the same value.", maxReadBytes),
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) RequiresApproval(string) bool { return false }
func (t *ReadFileTool) ParallelSafe(string) bool     { return true }

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
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
	if a.Offset < 0 {
		a.Offset = 0
	}
	limit := a.Limit
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}

	p := resolvePath(t.Cwd, a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()
	if a.Offset > 0 {
		if _, err := f.Seek(a.Offset, io.SeekStart); err != nil {
			return "", fmt.Errorf("read_file: seek: %w", err)
		}
	}

	// Read one extra byte so we can detect "more remains" vs "exact EOF".
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.EOF):
		// offset past EOF — return empty, no error.
		return "", nil
	case errors.Is(err, io.ErrUnexpectedEOF):
		// We read fewer bytes than buf; that's normal for short files.
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	truncated := int64(n) > limit
	if truncated {
		n = int(limit)
	}
	content := string(buf[:n])
	if truncated {
		content += "\n…[truncated]"
	}
	return content, nil
}

// WriteFileTool creates or overwrites a file. Always needs approval; the
// WriteOpts validator pre-rejects out-of-cwd, symlinked, or deny-listed
// paths *before* the approval modal opens, so the model can't trick a
// distracted user into approving a misleading path.
type WriteFileTool struct {
	Cwd       string
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
	p := resolvePath(t.Cwd, a.Path)
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
