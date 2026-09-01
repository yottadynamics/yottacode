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

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

const (
	defaultReadManyMaxFiles = 20

	// maxReadManyTotalBytes bounds the combined output of a single
	// read_many_files call. Each file is already capped at maxReadBytes,
	// but with up to defaultReadManyMaxFiles files that still allows one
	// call to return up to 10 MiB. A batch that size lands in history and
	// gets resent verbatim on every subsequent turn until the session is
	// summarized, so the aggregate needs its own, tighter ceiling.
	maxReadManyTotalBytes = 2 * 1024 * 1024 // 2 MiB
)

type DeleteFileTool struct {
	Cwd       *CwdRef
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
	p := resolvePath(t.Cwd.Get(), a.Path)
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
	Cwd       *CwdRef
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
	src := resolvePath(t.Cwd.Get(), a.Src)
	dst := resolvePath(t.Cwd.Get(), a.Dst)
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
	Cwd       *CwdRef
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
	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateWritePath(p, t.WriteOpts); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	return fmt.Sprintf("created directory %s", p), nil
}

type CopyFileTool struct {
	Cwd       *CwdRef
	WriteOpts WritePathOptions
	// DenyReadPaths gates the SOURCE against the credential read deny-list,
	// the same list read_file/read_many_files/grep use. copy_file is an
	// auto-approved read+write, so without this it would be a back door
	// around the read guard: copy ~/.ssh/id_rsa into a readable file, then
	// read that.
	DenyReadPaths []string
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
	src := resolvePath(t.Cwd.Get(), a.Src)
	dst := resolvePath(t.Cwd.Get(), a.Dst)
	// Source may live outside cwd (copying in is legitimate), but it must
	// still clear the credential read deny-list — otherwise copy_file is a
	// back door that reads ~/.ssh/id_rsa into a readable destination.
	// ValidateReadPath also resolves symlinks, so a symlinked source is
	// caught too.
	if err := ValidateReadPath(src, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("copy_file: source: %w", err)
	}
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
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *ReadManyFilesTool) Name() string { return "read_many_files" }
func (t *ReadManyFilesTool) Description() string {
	return fmt.Sprintf("Read multiple UTF-8 text files from disk in one call. Optional offset and limit apply to each file. Set anchors=true to prefix each returned line with line#anchor. Returns clearly separated sections per file. Combined output across all files in one call is capped at %d bytes; remaining files past the cap are listed but not read, and can be requested in a follow-up call.", maxReadManyTotalBytes)
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
			"offset":  map[string]any{"type": "integer", "description": "Byte offset to start each read from (default 0)"},
			"limit":   map[string]any{"type": "integer", "description": fmt.Sprintf("Max bytes per file (default %d)", maxReadBytes)},
			"anchors": map[string]any{"type": "boolean", "description": "When true, prefix each returned line with line#anchor before the tab-delimited content."},
		},
		"required": []string{"paths"},
	}
}
func (t *ReadManyFilesTool) RequiresApproval(string) bool { return false }
func (t *ReadManyFilesTool) ParallelSafe(string) bool     { return true }
func (t *ReadManyFilesTool) PreviewCall(argsJSON string) string {
	paths, _, _, anchors, _ := parseReadManyFilesArgs(argsJSON)
	if anchors {
		return fmt.Sprintf("read_many_files(%d paths, anchors=true)", len(paths))
	}
	return fmt.Sprintf("read_many_files(%d paths)", len(paths))
}
func (t *ReadManyFilesTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	pathsArg, offset, limitArg, anchors, err := parseReadManyFilesArgs(argsJSON)
	if err != nil {
		return "", fmt.Errorf("read_many_files: invalid args: %w", err)
	}
	if len(pathsArg) == 0 {
		return "", fmt.Errorf("read_many_files: paths is required")
	}
	if len(pathsArg) > defaultReadManyMaxFiles {
		return "", fmt.Errorf("read_many_files: too many paths (%d > %d)", len(pathsArg), defaultReadManyMaxFiles)
	}
	if offset < 0 {
		offset = 0
	}
	limit := limitArg
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	paths := append([]string(nil), pathsArg...)
	sort.Strings(paths)
	var b strings.Builder
	for i, rel := range paths {
		if b.Len() >= maxReadManyTotalBytes {
			skipped := paths[i:]
			fmt.Fprintf(&b, "\n…[read budget exceeded (%d bytes); %d file(s) not read: %s — request them in a separate call]",
				maxReadManyTotalBytes, len(skipped), strings.Join(skipped, ", "))
			break
		}
		p := resolvePath(t.Cwd.Get(), rel)
		if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
			return "", fmt.Errorf("read_many_files: %w", err)
		}
		f, err := os.Open(p)
		if err != nil {
			return "", fmt.Errorf("read_many_files: %s: %w", p, err)
		}
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
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
			// Zero bytes read: an empty file, or an offset at/after EOF.
			// That's empty content, not an error — read_file returns
			// empty content for an empty file, so match it here. Erroring
			// would abort the whole batch over a single empty file.
			n = 0
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
		if anchors {
			receipt, err := hashline.HashSpan(buf[:n], 0, n)
			if err != nil {
				return "", fmt.Errorf("read_many_files: %s: %w", p, err)
			}
			fmt.Fprintf(&b, "# hashline path=%s offset=%d length=%d hash=%s\n", rel, offset, n, receipt.Hash)
			text := string(buf[:n])
			text = strings.TrimSuffix(text, "\n")
			if text != "" {
				lines := strings.Split(text, "\n")
				anchored := buildAnchoredLines(lines, 1)
				for j, line := range anchored {
					fmt.Fprintf(&b, "%6d#%s\t%s", line.LineNumber, line.Hash, line.Content)
					if j < len(anchored)-1 || truncated {
						b.WriteByte('\n')
					}
				}
			}
		} else {
			b.Write(buf[:n])
		}
		if truncated {
			b.WriteString("\n…[truncated]")
		}
	}
	return b.String(), nil
}

func parseReadManyFilesArgs(argsJSON string) ([]string, int64, int64, bool, error) {
	var raw struct {
		Paths   json.RawMessage `json:"paths"`
		Offset  int64           `json:"offset"`
		Limit   int64           `json:"limit"`
		Anchors bool            `json:"anchors"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, 0, 0, false, err
	}
	var paths []string
	if err := json.Unmarshal(raw.Paths, &paths); err != nil {
		var one string
		if err := json.Unmarshal(raw.Paths, &one); err != nil {
			return nil, 0, 0, false, fmt.Errorf("paths must be an array of strings or a single string")
		}
		paths = []string{one}
	}
	for i := range paths {
		paths[i] = strings.TrimSpace(paths[i])
	}
	return paths, raw.Offset, raw.Limit, raw.Anchors, nil
}
