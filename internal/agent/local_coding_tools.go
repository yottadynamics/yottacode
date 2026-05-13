package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultReadManyMaxFiles = 20
)

type DeleteFileTool struct {
	Cwd       string
	WriteOpts WritePathOptions
}

func (t *DeleteFileTool) Name() string { return "delete_file" }
func (t *DeleteFileTool) Description() string {
	return "Delete a file or an empty directory at the given path. Returns a short confirmation on success."
}
func (t *DeleteFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "File or empty directory to delete"},
		},
		"required": []string{"path"},
	}
}
func (t *DeleteFileTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports the target so /checkpoints can recreate the
// deleted file on rewind.
func (t *DeleteFileTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Path == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Path)}
}

func (t *DeleteFileTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("delete_file(%s)", a.Path)
}
func (t *DeleteFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("delete_file: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("delete_file: path is required")
	}
	p := resolvePath(t.Cwd, a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("delete_file: %w", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("delete_file: %w", err)
	}
	if info.IsDir() {
		if err := os.Remove(p); err != nil {
			return "", fmt.Errorf("delete_file: %w", err)
		}
		return fmt.Sprintf("deleted directory %s", p), nil
	}
	if err := os.Remove(p); err != nil {
		return "", fmt.Errorf("delete_file: %w", err)
	}
	return fmt.Sprintf("deleted file %s", p), nil
}

type MoveFileTool struct {
	Cwd       string
	WriteOpts WritePathOptions
}

func (t *MoveFileTool) Name() string { return "move_file" }
func (t *MoveFileTool) Description() string {
	return "Move or rename a file or directory. Parent directories for the destination are created if needed."
}
func (t *MoveFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"src": map[string]any{"type": "string", "description": "Source path"},
			"dst": map[string]any{"type": "string", "description": "Destination path"},
		},
		"required": []string{"src", "dst"},
	}
}
func (t *MoveFileTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports both src (so we can recreate it on rewind)
// and dst (so we can remove the moved-to file on rewind).
func (t *MoveFileTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil
	}
	var out []string
	if a.Src != "" {
		out = append(out, resolvePath(cwd, a.Src))
	}
	if a.Dst != "" {
		out = append(out, resolvePath(cwd, a.Dst))
	}
	return out
}

func (t *MoveFileTool) PreviewCall(argsJSON string) string {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("move_file(%s -> %s)", a.Src, a.Dst)
}
func (t *MoveFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("move_file: invalid args: %w", err)
	}
	if a.Src == "" || a.Dst == "" {
		return "", fmt.Errorf("move_file: src and dst are required")
	}
	src := resolvePath(t.Cwd, a.Src)
	dst := resolvePath(t.Cwd, a.Dst)
	// Both source (we're removing it) and destination get validated.
	if err := ValidateWritePath(src, t.WriteOpts); err != nil {
		return "", fmt.Errorf("move_file: source: %w", err)
	}
	if err := ValidateWritePath(dst, t.WriteOpts); err != nil {
		return "", fmt.Errorf("move_file: destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("move_file: mkdir: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("move_file: %w", err)
	}
	return fmt.Sprintf("moved %s to %s", src, dst), nil
}

type MkdirTool struct {
	Cwd       string
	WriteOpts WritePathOptions
}

func (t *MkdirTool) Name() string { return "mkdir" }
func (t *MkdirTool) Description() string {
	return "Create a directory and any missing parents. Returns a short confirmation on success."
}
func (t *MkdirTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Directory to create"},
		},
		"required": []string{"path"},
	}
}
func (t *MkdirTool) RequiresApproval(string) bool { return true }
func (t *MkdirTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("mkdir(%s)", a.Path)
}
func (t *MkdirTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("mkdir: invalid args: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("mkdir: path is required")
	}
	p := resolvePath(t.Cwd, a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	return fmt.Sprintf("created directory %s", p), nil
}

type CopyFileTool struct {
	Cwd       string
	WriteOpts WritePathOptions
}

func (t *CopyFileTool) Name() string { return "copy_file" }
func (t *CopyFileTool) Description() string {
	return "Copy a file from src to dst. Parent directories for the destination are created if needed."
}
func (t *CopyFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"src": map[string]any{"type": "string", "description": "Source file path"},
			"dst": map[string]any{"type": "string", "description": "Destination file path"},
		},
		"required": []string{"src", "dst"},
	}
}
func (t *CopyFileTool) RequiresApproval(string) bool { return true }

// PathsToSnapshot reports only the destination — src is read-only here.
func (t *CopyFileTool) PathsToSnapshot(cwd, argsJSON string) []string {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Dst == "" {
		return nil
	}
	return []string{resolvePath(cwd, a.Dst)}
}

func (t *CopyFileTool) PreviewCall(argsJSON string) string {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("copy_file(%s -> %s)", a.Src, a.Dst)
}
func (t *CopyFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("copy_file: invalid args: %w", err)
	}
	if a.Src == "" || a.Dst == "" {
		return "", fmt.Errorf("copy_file: src and dst are required")
	}
	src := resolvePath(t.Cwd, a.Src)
	dst := resolvePath(t.Cwd, a.Dst)
	// Only the destination is being written; source is read-only here so
	// we don't validate it (lets users copy from outside cwd into cwd).
	if err := ValidateWritePath(dst, t.WriteOpts); err != nil {
		return "", fmt.Errorf("copy_file: destination: %w", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("copy_file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("copy_file: source %s is a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("copy_file: mkdir: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("copy_file: %w", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("copy_file: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", fmt.Errorf("copy_file: %w", err)
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("copy_file: chmod: %w", err)
	}
	return fmt.Sprintf("copied %s to %s", src, dst), nil
}

type ReadManyFilesTool struct {
	Cwd           string
	DenyReadPaths []string
}

func (t *ReadManyFilesTool) Name() string { return "read_many_files" }
func (t *ReadManyFilesTool) Description() string {
	return "Read multiple UTF-8 text files from disk in one call. Optional offset and limit apply to each file. Returns clearly separated sections per file."
}
func (t *ReadManyFilesTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Files to read",
			},
			"offset": map[string]any{"type": "integer", "description": "Byte offset to start each read from (default 0)"},
			"limit":  map[string]any{"type": "integer", "description": fmt.Sprintf("Max bytes per file (default %d)", maxReadBytes)},
		},
		"required": []string{"paths"},
	}
}
func (t *ReadManyFilesTool) RequiresApproval(string) bool { return false }
func (t *ReadManyFilesTool) ParallelSafe(string) bool     { return true }
func (t *ReadManyFilesTool) PreviewCall(argsJSON string) string {
	var a struct {
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("read_many_files(%d paths)", len(a.Paths))
}
func (t *ReadManyFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Paths  []string `json:"paths"`
		Offset int64    `json:"offset"`
		Limit  int64    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("read_many_files: invalid args: %w", err)
	}
	if len(a.Paths) == 0 {
		return "", fmt.Errorf("read_many_files: paths is required")
	}
	if len(a.Paths) > defaultReadManyMaxFiles {
		return "", fmt.Errorf("read_many_files: too many paths (%d > %d)", len(a.Paths), defaultReadManyMaxFiles)
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	limit := a.Limit
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	paths := append([]string(nil), a.Paths...)
	sort.Strings(paths)
	var b strings.Builder
	for i, rel := range paths {
		p := resolvePath(t.Cwd, rel)
		if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
			return "", fmt.Errorf("read_many_files: %w", err)
		}
		f, err := os.Open(p)
		if err != nil {
			return "", fmt.Errorf("read_many_files: %s: %w", p, err)
		}
		if a.Offset > 0 {
			if _, err := f.Seek(a.Offset, io.SeekStart); err != nil {
				f.Close()
				return "", fmt.Errorf("read_many_files: %s: seek: %w", p, err)
			}
		}
		buf := make([]byte, limit+1)
		n, err := io.ReadFull(f, buf)
		f.Close()
		switch err {
		case nil:
		case io.EOF:
			if a.Offset > 0 {
				n = 0
			} else {
				return "", fmt.Errorf("read_many_files: %s: %w", p, err)
			}
		case io.ErrUnexpectedEOF:
		default:
			return "", fmt.Errorf("read_many_files: %s: %w", p, err)
		}
		truncated := int64(n) > limit
		if truncated {
			n = int(limit)
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "==> %s <==\n", rel)
		b.Write(buf[:n])
		if truncated {
			b.WriteString("\n…[truncated]")
		}
		if n == 0 && a.Offset > 0 {
			b.WriteString("")
		}
	}
	return b.String(), nil
}
