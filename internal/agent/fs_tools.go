package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/edit/hashline"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

const (
	// maxReadBytes caps how much content a single read *returns*. With
	// the default 2000-line window and ~80-char lines this leaves
	// comfortable headroom; it also bounds the worst-case cost of a
	// single pathologically long line.
	//
	// It deliberately does not cap how far into a file the window may
	// start. It used to: the reader took a fixed maxReadBytes prefix and
	// indexed offset into that, so every line past the prefix was
	// unreachable and asking for one returned an empty string —
	// indistinguishable from a genuine end-of-file. Lines are now
	// skipped by streaming, so reaching a distant offset costs time
	// rather than memory.
	maxReadBytes = 512 * 1024 // 512 KiB

	// readWindowBufSize is the buffered-reader size used while streaming
	// to the requested window. Large enough that ordinary lines never
	// need a second pass; overlong lines are consumed incrementally
	// rather than by growing this.
	readWindowBufSize = 64 * 1024

	// defaultReadLines mirrors Claude Code's Read tool: 2000 lines is
	// the implicit limit when the model omits an explicit `limit`.
	defaultReadLines = 2000
)

// ReadFileTool lets the model fetch local file contents. Read-only, no
// approval. DenyReadPaths blocks a small set of credential-bearing
// locations (see DefaultDenyReadPaths) so prompt injection can't
// silently exfiltrate keys; everything else is fair game.
type ReadFileTool struct {
	Cwd            *CwdRef
	DenyReadPaths  []string
	SupportsImages bool
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read a file. For text files, output is `cat -n` style: every line is prefixed with its 1-indexed line number and a tab, or `line#anchor\tcontent` when anchors=true. " +
		"For image files (png, jpg, gif, webp), the image is returned as visual content the model can see directly. " +
		"Optional offset (1-indexed start line, default 1) and limit (lines, default 2000) apply to text files only. " +
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
			"anchors": map[string]any{
				"type":        "boolean",
				"description": "When true, prefix each text line with line#anchor before the tab-delimited content.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) RequiresApproval(string) bool { return false }
func (t *ReadFileTool) ParallelSafe(string) bool     { return true }

type readFileArgs struct {
	Path    string `json:"path"`
	Offset  int    `json:"offset"` // 1-indexed start line
	Limit   int    `json:"limit"`  // max lines to return
	Anchors bool   `json:"anchors"`
}

func (t *ReadFileTool) PreviewCall(argsJSON string) string {
	var a readFileArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Offset == 0 && a.Limit == 0 && !a.Anchors {
		return fmt.Sprintf("read_file(%s)", a.Path)
	}
	return fmt.Sprintf("read_file(%s, offset=%d, limit=%d, anchors=%t)", a.Path, a.Offset, a.Limit, a.Anchors)
}

func (t *ReadFileTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	res, err := t.ExecuteMultimodal(ctx, argsJSON)
	return res.Content, err
}

func (t *ReadFileTool) readText(ctx context.Context, a readFileArgs) (string, error) {
	startLine := max(a.Offset, 1)
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

	selected, selectedBytes, startByte, truncated, err := readLineWindow(ctx, f, startLine, limit, maxReadBytes)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	if len(selected) == 0 {
		return "", nil
	}

	anchored := buildAnchoredLines(selected, startLine)
	var sb strings.Builder
	if a.Anchors {
		receipt, err := hashline.HashSpan(selectedBytes, 0, len(selectedBytes))
		if err != nil {
			return "", fmt.Errorf("read_file: %w", err)
		}
		fmt.Fprintf(&sb, "# hashline path=%s offset=%d length=%d hash=%s\n", a.Path, startByte, len(selectedBytes), receipt.Hash)
	}
	for i, line := range anchored {
		if a.Anchors {
			fmt.Fprintf(&sb, "%6d#%s\t%s", line.LineNumber, line.Hash, line.Content)
		} else {
			fmt.Fprintf(&sb, "%6d\t%s", line.LineNumber, line.Content)
		}
		if i < len(anchored)-1 || truncated {
			sb.WriteByte('\n')
		}
	}
	if truncated {
		sb.WriteString("…[truncated]")
	}
	return sb.String(), nil
}

// readLineWindow streams r and returns the lines in
// [startLine, startLine+limit), plus whether any content follows that
// window (which is what earns the trailing truncation marker).
//
// Lines before the window are read and discarded rather than retained,
// so the window can begin anywhere in the file while held memory stays
// bounded by maxBytes. Reaching a distant offset therefore costs time,
// not memory — the same trade read_document makes for paging.
//
// A window starting past the last line returns no lines and no error,
// matching the tool's long-standing "offset past EOF yields empty"
// contract.
func readLineWindow(ctx context.Context, r io.Reader, startLine, limit, maxBytes int) ([]string, []byte, int, bool, error) {
	br := bufio.NewReaderSize(r, readWindowBufSize)
	startByte := 0

	for n := 1; n < startLine; n++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, false, err
		}
		skipped, err := discardLine(br)
		startByte += skipped
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, startByte, false, nil
			}
			return nil, nil, startByte, false, err
		}
	}

	var (
		lines []string
		used  int
		span  []byte
	)
	for len(lines) < limit {
		if err := ctx.Err(); err != nil {
			return nil, nil, startByte, false, err
		}
		line, readErr := br.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, nil, startByte, false, readErr
		}
		atEOF := errors.Is(readErr, io.EOF)
		if line == "" && atEOF {
			return lines, span, startByte, false, nil
		}
		lineBytes := []byte(line)
		lineText := strings.TrimSuffix(line, "\n")

		if used+len(lineBytes) > maxBytes {
			// A single line that alone blows the budget still has to
			// return something rather than nothing. The receipt covers exactly
			// the returned byte prefix, not the full overlong source line.
			if len(lines) == 0 {
				keep := min(len(lineBytes), maxBytes)
				span = append(span, lineBytes[:keep]...)
				lines = append(lines, strings.TrimSuffix(string(lineBytes[:keep]), "\n"))
			}
			return lines, span, startByte, true, nil
		}
		lines = append(lines, lineText)
		span = append(span, lineBytes...)
		used += len(lineBytes)
		if atEOF {
			return lines, span, startByte, false, nil
		}
	}

	// Stopped on the line limit — the marker depends on whether
	// anything is actually left after the window.
	_, peekErr := br.Peek(1)
	return lines, span, startByte, peekErr == nil, nil
}

// discardLine consumes bytes through the next newline without retaining
// them. ReadSlice reports ErrBufferFull when a line is longer than the
// buffer, so looping on that skips even a pathologically long line in
// bounded memory.
func discardLine(br *bufio.Reader) (int, error) {
	total := 0
	for {
		chunk, err := br.ReadSlice('\n')
		total += len(chunk)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return total, err
	}
}

const maxImageBytes = 20 * 1024 * 1024 // 20 MiB

var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

func imageMediaType(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	mt, ok := imageMediaTypes[ext]
	return mt, ok
}

func (t *ReadFileTool) ExecuteMultimodal(ctx context.Context, argsJSON string) (MultimodalResult, error) {
	var a readFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return MultimodalResult{}, fmt.Errorf("read_file: invalid args: %w", err)
	}
	if a.Path == "" {
		return MultimodalResult{}, fmt.Errorf("read_file: path is required")
	}

	p := resolvePath(t.Cwd.Get(), a.Path)
	if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
		return MultimodalResult{}, fmt.Errorf("read_file: %w", err)
	}

	mediaType, isImage := imageMediaType(p)
	if !isImage {
		text, err := t.readText(ctx, a)
		return MultimodalResult{Content: text}, err
	}

	info, err := os.Stat(p)
	if err != nil {
		return MultimodalResult{}, fmt.Errorf("read_file: %w", err)
	}
	label := fmt.Sprintf("[image: %s, %s, %d bytes]", filepath.Base(p), mediaType, info.Size())

	if !t.SupportsImages {
		return MultimodalResult{
			Content: label + " — current model does not support image input",
		}, nil
	}
	if info.Size() > maxImageBytes {
		return MultimodalResult{
			Content: label + " — image exceeds 20 MiB size limit",
		}, nil
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return MultimodalResult{}, fmt.Errorf("read_file: %w", err)
	}

	return MultimodalResult{
		Content: label,
		Images:  []adapter.ImageBlock{{Data: data, MediaType: mediaType}},
	}, nil
}

// WriteFileTool creates or overwrites a file. Always needs approval; the
// WriteOpts validator pre-rejects out-of-cwd, symlinked, or deny-listed
// paths *before* the approval modal opens, so the model can't trick a
// distracted user into approving a misleading path.
type WriteFileTool struct {
	Cwd        *CwdRef
	WriteOpts  WritePathOptions
	LSPManager *lspci.Manager
	LSPServers map[string][]string
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
	msg := fmt.Sprintf("wrote %d bytes to %s", len(a.Content), p)
	if note := notifyLSPFileChanged(ctx, t.Cwd, t.LSPManager, t.LSPServers, p, a.Content); note != "" {
		msg += "\n" + note
	}
	return msg, nil
}
