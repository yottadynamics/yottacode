package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

const (
	defaultReadManyMaxFiles = 20

	// A batch that size lands in history and gets resent verbatim on every
	// subsequent turn until the session is summarized, so keep the aggregate
	// ceiling deliberately small. Larger files should be requested separately.
	maxReadManyTotalBytes = 512 * 1024 // 512 KiB

	// readManyMinSectionBytes is the least content budget worth opening another
	// file for. Below it the remaining files are listed as not read instead of
	// being returned as slivers.
	readManyMinSectionBytes = 1024

	// readManyTruncMarker ends a section whose content was cut short. The TUI
	// keys off this exact suffix when it renders tool cards.
	readManyTruncMarker = "\n…[truncated]"

	// readManyPrefixChunk is the buffer size used to count lines before a byte
	// offset, so a distant offset costs time rather than memory.
	readManyPrefixChunk = 32 * 1024
)

// ReadManyFilesTool reads several UTF-8 text files in one call.
//
// Contract:
//   - Every path is checked against the read deny list before any file is
//     opened. A denied path refuses the whole call: denial is never reported
//     inline, so a partial result cannot leak alongside it.
//   - Sections are emitted in sorted path order, so identical requests produce
//     identical (cache-friendly) output.
//   - offset and limit are byte counts applied to each file. A window cut short
//     by limit or by the aggregate budget ends on a line boundary (or, inside a
//     single overlong line, on a rune boundary), so the continuation offset
//     lands cleanly. offset itself may land mid-line.
//   - With anchors=true, line numbers are absolute (counted from the start of
//     the file, not the window) and each section's hashline receipt covers
//     exactly the bytes rendered as lines in that section.
//   - The file sections together never exceed maxReadManyTotalBytes. Files that
//     no longer fit are listed in a trailing note so the caller can request
//     them separately; that note only echoes paths the caller supplied.
//   - A file that cannot be read (missing, unreadable, not a regular file) gets
//     an inline "[error: …]" section and does not abort the batch; binary or
//     non-UTF-8 content gets an inline "[skipped: …]" section. If no file could
//     be read at all, the call returns an error instead.
type ReadManyFilesTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *ReadManyFilesTool) Name() string { return "read_many_files" }
func (t *ReadManyFilesTool) Description() string {
	return fmt.Sprintf("Read multiple UTF-8 text files from disk in one call. offset and limit are byte counts applied to each file; a cut-short file ends on a line boundary. Set anchors=true to prefix each returned line with its absolute line#anchor and emit a hashline receipt per file. Sections come back in sorted path order. Combined output is capped at %d bytes; files that do not fit are listed as not read and can be requested in a follow-up call. A file that cannot be read, or is binary, gets an inline [error:]/[skipped:] section instead of failing the batch; a path on the read deny list refuses the whole call.", maxReadManyTotalBytes)
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
			"offset":  map[string]any{"type": "integer", "description": "Byte offset to start each read from (default 0); may land mid-line"},
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

	// Deny-list every path before touching the filesystem: a denied entry
	// refuses the whole call, and nothing has been read yet that could leak.
	resolved := make([]string, len(paths))
	for i, rel := range paths {
		p := resolvePath(t.Cwd.Get(), rel)
		if err := ValidateReadPath(p, t.DenyReadPaths); err != nil {
			return "", fmt.Errorf("read_many_files: %w", err)
		}
		resolved[i] = p
	}

	var (
		b        strings.Builder
		failures []string
		sections int
		readable int
	)
	for i, rel := range paths {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("read_many_files: %w", err)
		}
		header := "==> " + rel + " <==\n"
		sepLen := 0
		if sections > 0 {
			sepLen = 1
		}
		budget := maxReadManyTotalBytes - b.Len() - sepLen - len(header)
		if budget-readManyBodyReserve(rel, offset, anchors) < readManyMinSectionBytes {
			skipped := paths[i:]
			fmt.Fprintf(&b, "\n…[read budget exceeded (%d bytes); %d file(s) not read: %s — request them in a separate call]",
				maxReadManyTotalBytes, len(skipped), strings.Join(skipped, ", "))
			break
		}
		body, failure, err := readManyOne(ctx, resolved[i], rel, offset, limit, anchors, budget)
		if err != nil {
			return "", fmt.Errorf("read_many_files: %w", err)
		}
		if failure != "" {
			failures = append(failures, rel+": "+failure)
		} else {
			readable++
		}
		if sections > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(header)
		b.WriteString(body)
		sections++
	}
	if readable == 0 && len(failures) > 0 {
		return "", fmt.Errorf("read_many_files: %s", strings.Join(failures, "; "))
	}
	return b.String(), nil
}

// readManyOne reads and renders one file's section body within budget bytes.
// When the file cannot be read, body is the inline "[error: …]" text and
// failure carries the bare reason. A non-nil err is reserved for context
// cancellation, which aborts the whole call.
func readManyOne(ctx context.Context, path, rel string, offset, limit int64, anchors bool, budget int) (body, failure string, err error) {
	f, info, openErr := openRegularFile(path)
	if openErr != nil {
		msg := readManyFailure(openErr)
		return "[error: " + msg + "]", msg, nil
	}
	defer f.Close()

	w, readErr := readManyWindowFrom(ctx, f, info.Size(), offset, limit, anchors)
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		msg := readManyFailure(readErr)
		return "[error: " + msg + "]", msg, nil
	}
	if reason := nonTextReason(w.data, offset > 0); reason != "" {
		return nonTextSkipMessage(reason), "", nil
	}
	// A byte offset may split a UTF-8 rune. Do not emit an invalid leading
	// fragment: drop the continuation bytes and adjust the receipt offset so
	// the rendered text remains valid and hashable.
	if offset > 0 && len(w.data) > 0 && !utf8.RuneStart(w.data[0]) {
		drop := 0
		for drop < len(w.data) && drop < utf8.UTFMax-1 && !utf8.RuneStart(w.data[drop]) {
			drop++
		}
		w.data = w.data[drop:]
		offset += int64(drop)
	}
	body, renderErr := renderReadManyBody(rel, w, offset, anchors, budget)
	if renderErr != nil {
		msg := readManyFailure(renderErr)
		return "[error: " + msg + "]", msg, nil
	}
	return body, "", nil
}

// readManyFailure reduces an error to its bare reason: the path is already in
// the section header, so "open /abs/x: no such file or directory" becomes
// "no such file or directory".
func readManyFailure(err error) string {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

// readManyWindow is the raw slice of one file selected by offset and limit.
type readManyWindow struct {
	data      []byte // ends on a line (or rune) boundary when truncated
	startLine int    // absolute 1-indexed line number of the first line in data
	truncated bool   // more content follows data in the file
}

// readManyWindowFrom reads at most limit bytes of f starting at offset.
// size is only a preallocation hint; the read itself is bounded by limit, so a
// file that grows underneath us is still capped and correctly marked truncated.
func readManyWindowFrom(ctx context.Context, f *os.File, size, offset, limit int64, anchors bool) (readManyWindow, error) {
	w := readManyWindow{startLine: 1}
	switch {
	case offset == 0:
	case anchors:
		// Anchors embed absolute line numbers, so the newlines before offset
		// must be counted. Reading the prefix also positions f at offset.
		read, newlines, err := skipCountingLines(ctx, f, offset)
		if err != nil {
			return w, err
		}
		if read < offset {
			return w, nil // offset is at or past EOF: empty window
		}
		w.startLine = newlines + 1
	default:
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return w, err
		}
	}

	hint := min(limit+1, max(size-offset, 0)+1)
	buf := bytes.NewBuffer(make([]byte, 0, hint))
	if _, err := buf.ReadFrom(io.LimitReader(f, limit+1)); err != nil {
		return w, err
	}
	w.data = buf.Bytes()
	if int64(len(w.data)) > limit {
		w.truncated = true
		w.data = cutWindow(w.data, int(limit))
	}
	return w, nil
}

// skipCountingLines consumes up to n bytes from r and reports how many it
// read and how many newlines they contained. It stops early, without error, at
// EOF.
func skipCountingLines(ctx context.Context, r io.Reader, n int64) (read int64, newlines int, err error) {
	buf := make([]byte, readManyPrefixChunk)
	for read < n {
		if err := ctx.Err(); err != nil {
			return read, newlines, err
		}
		want := min(int64(len(buf)), n-read)
		m, rerr := r.Read(buf[:want])
		newlines += bytes.Count(buf[:m], []byte{'\n'})
		read += int64(m)
		if rerr == io.EOF {
			return read, newlines, nil
		}
		if rerr != nil {
			return read, newlines, rerr
		}
	}
	return read, newlines, nil
}

// cutWindow shortens data to at most max bytes without leaving a partial
// trailing line or rune: it backs up to the last newline, and only when the
// window holds no newline at all (one overlong line) to a rune boundary.
func cutWindow(data []byte, max int) []byte {
	if len(data) <= max {
		return data
	}
	data = data[:max]
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return data[:i+1]
	}
	return trimPartialRune(data)
}

// readManyBodyReserve is the worst-case size of everything a section body adds
// around its content: the truncation marker and, with anchors, the receipt line.
func readManyBodyReserve(rel string, offset int64, anchors bool) int {
	n := len(readManyTruncMarker)
	if anchors {
		n += len("# hashline path=") + len(rel) +
			len(" offset=") + len(strconv.FormatInt(offset, 10)) +
			len(" length=") + len(strconv.Itoa(maxReadBytes)) +
			len(" hash=") + hashline.HashHexLength +
			len(" ends_with_newline=false") + len("\n")
	}
	return n
}

// anchoredLineText renders one anchored line without its trailing newline.
func anchoredLineText(lineNum int, content []byte) string {
	return fmt.Sprintf("%6d#%s\t%s", lineNum, anchorHashForLine(lineNum, string(content)), content)
}

// renderReadManyBody formats w for output within budget bytes (which covers
// the body only, not the section header). If the window does not fit it is
// cut again on a line boundary and marked truncated. With anchors, the
// receipt is computed over exactly the bytes that were rendered as lines.
func renderReadManyBody(rel string, w readManyWindow, offset int64, anchors bool, budget int) (string, error) {
	budget -= readManyBodyReserve(rel, offset, anchors)
	data, truncated := w.data, w.truncated

	if !anchors {
		if len(data) > budget {
			data, truncated = cutWindow(data, budget), true
		}
		if truncated {
			return string(data) + readManyTruncMarker, nil
		}
		return string(data), nil
	}

	var (
		lines  strings.Builder
		nLines int
		kept   int // raw bytes of data rendered so far; the receipt covers data[:kept]
	)
	lineNum := w.startLine
	for pos := 0; pos < len(data); {
		end := len(data)
		if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
			end = pos + i + 1
		}
		content := bytes.TrimSuffix(data[pos:end], []byte("\n"))
		text := anchoredLineText(lineNum, content)
		sep := 0
		if nLines > 0 {
			sep = 1
		}
		if lines.Len()+sep+len(text) > budget {
			if nLines == 0 {
				// One line larger than the whole budget: keep a rune-aligned
				// prefix so the call still makes progress. Its anchor hashes the
				// prefix, so it will not match the full line and edit_anchored
				// rejects it as stale rather than editing the wrong text.
				room := budget - (len(text) - len(content))
				if room > 0 {
					part := trimPartialRune(content[:min(room, len(content))])
					lines.WriteString(anchoredLineText(lineNum, part))
					nLines++
					kept = len(part)
				}
			}
			truncated = true
			break
		}
		if nLines > 0 {
			lines.WriteByte('\n')
		}
		lines.WriteString(text)
		nLines++
		kept = end
		lineNum++
		pos = end
	}

	receipt, err := hashline.HashSpan(data, 0, kept)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# hashline path=%s offset=%d length=%d hash=%s ends_with_newline=%t\n", rel, offset, kept, receipt.Hash, spanEndsWithNewline(data[:kept]))
	out.WriteString(lines.String())
	if truncated {
		out.WriteString(readManyTruncMarker)
	}
	return out.String(), nil
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
