package compaction

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// File-operation tracking and progress-note parsing shared by the in-loop
// (internal/agent) and turn-boundary (internal/tui) compaction paths.
//
// The model-written summary is lossy about which files were touched, so
// compaction also records them deterministically from the dropped tool calls
// and appends a capped <files> block to the progress note. A later compaction
// splits the note back apart: the prose goes to the summarizer as
// <previous-summary> (iterative update, so notes don't stack), and the file
// set is merged with the newly dropped calls.

const (
	// ProgressMarker labels the progress note folded into a task message by
	// in-loop compaction, so the model (and a later compaction) can tell the
	// text is a compaction artifact rather than part of the original request.
	ProgressMarker = "[Earlier progress compacted to stay within the context window]"
	// SessionSummaryPrefix opens the synthetic assistant message that the TUI
	// path writes ("[Session summary — compressed at turn N]").
	SessionSummaryPrefix = "[Session summary — compressed at"

	// LatestRequestOpen/Close wrap the user request kept verbatim on the task
	// message after a mid-turn cut.
	LatestRequestOpen  = "<latest-request>"
	LatestRequestClose = "</latest-request>"

	maxCompactionFiles = 20
	filesOpenTag       = "<files>"
	filesCloseTag      = "</files>"
)

// FileOp is a bitset of how a file was touched.
type FileOp uint8

const (
	OpRead FileOp = 1 << iota
	OpWrite
)

var (
	readFileTools = map[string]bool{"read_file": true, "read_many_files": true, "read_document": true}
	// Tools that modify the file named by their path argument.
	writeFileTools = map[string]bool{
		"write_file": true, "edit_file": true, "edit_anchored": true,
		"apply_diff": true, "apply_hashline": true,
	}
)

// ExtractFileOps collects the files read/modified by tool calls in msgs.
func ExtractFileOps(msgs []adapter.Message) map[string]FileOp {
	ops := map[string]FileOp{}
	for _, m := range msgs {
		if m.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			var op FileOp
			switch {
			case readFileTools[tc.Name]:
				op = OpRead
			case writeFileTools[tc.Name]:
				op = OpWrite
			default:
				continue
			}
			for _, p := range callPaths(tc.ArgsJSON) {
				ops[p] |= op
			}
		}
	}
	return ops
}

func callPaths(argsJSON string) []string {
	var a struct {
		Path     string   `json:"path"`
		FilePath string   `json:"file_path"`
		Paths    []string `json:"paths"`
	}
	if json.Unmarshal([]byte(argsJSON), &a) != nil {
		return nil
	}
	var out []string
	for _, p := range append([]string{a.Path, a.FilePath}, a.Paths...) {
		// Paths come from model-written arguments and are re-parsed line by
		// line from the <files> block, so refuse anything that could break it.
		if p = strings.TrimSpace(p); p != "" && !strings.ContainsAny(p, "\r\n<>") {
			out = append(out, p)
		}
	}
	return out
}

func mergeFileOps(dst, src map[string]FileOp) {
	for p, op := range src {
		dst[p] |= op
	}
}

// FormatFilesBlock renders ops as a <files> block, modified files first, capped
// at maxCompactionFiles. Returns "" when ops is empty.
func FormatFilesBlock(ops map[string]FileOp) string {
	if len(ops) == 0 {
		return ""
	}
	paths := make([]string, 0, len(ops))
	for p := range ops {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		wi, wj := ops[paths[i]]&OpWrite != 0, ops[paths[j]]&OpWrite != 0
		if wi != wj {
			return wi
		}
		return paths[i] < paths[j]
	})
	var b strings.Builder
	b.WriteString(filesOpenTag + "\n")
	for i, p := range paths {
		if i == maxCompactionFiles {
			fmt.Fprintf(&b, "(+%d more)\n", len(paths)-i)
			break
		}
		label := "Read"
		switch ops[p] {
		case OpWrite:
			label = "Write"
		case OpRead | OpWrite:
			label = "RW"
		}
		fmt.Fprintf(&b, "(%s) %s\n", label, p)
	}
	b.WriteString(filesCloseTag)
	return b.String()
}

// SplitNote separates a task message that already carries a
// compaction progress note into the original task text, the previous
// summary prose, and the previously recorded file set.
func SplitNote(content string) (task, prevSummary string, prevFiles map[string]FileOp) {
	i := strings.Index(content, ProgressMarker)
	if i < 0 {
		return content, "", nil
	}
	task = strings.TrimSpace(content[:i])
	note := strings.TrimSpace(content[i+len(ProgressMarker):])
	// The verbatim latest request is regenerated on every compaction.
	if j := strings.Index(note, LatestRequestOpen); j >= 0 {
		if k := strings.Index(note[j:], LatestRequestClose); k >= 0 {
			note = strings.TrimSpace(note[:j] + note[j+k+len(LatestRequestClose):])
		}
	}
	if j := strings.Index(note, filesOpenTag); j >= 0 {
		if k := strings.Index(note[j:], filesCloseTag); k >= 0 {
			prevFiles = parseFilesBlock(note[j+len(filesOpenTag) : j+k])
			note = strings.TrimSpace(note[:j] + note[j+k+len(filesCloseTag):])
		}
	}
	return task, note, prevFiles
}

func parseFilesBlock(body string) map[string]FileOp {
	ops := map[string]FileOp{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		var op FileOp
		var rest string
		switch {
		case strings.HasPrefix(line, "(Read) "):
			op, rest = OpRead, line[len("(Read) "):]
		case strings.HasPrefix(line, "(Write) "):
			op, rest = OpWrite, line[len("(Write) "):]
		case strings.HasPrefix(line, "(RW) "):
			op, rest = OpRead|OpWrite, line[len("(RW) "):]
		default:
			continue
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			ops[rest] |= op
		}
	}
	return ops
}

// SplitSessionSummary parses the synthetic assistant message written by the
// TUI path ("[Session summary — compressed at turn N]\n\n<body>"), returning
// the summary prose (without the marker line or <files> block) and the
// recorded file set. ok is false when content is not such a message.
func SplitSessionSummary(content string) (prev string, files map[string]FileOp, ok bool) {
	if !strings.HasPrefix(content, SessionSummaryPrefix) {
		return "", nil, false
	}
	body := content
	if i := strings.Index(body, "\n\n"); i >= 0 {
		body = body[i+2:]
	}
	body = strings.TrimSpace(body)
	if j := strings.Index(body, filesOpenTag); j >= 0 {
		if k := strings.Index(body[j:], filesCloseTag); k >= 0 {
			files = parseFilesBlock(body[j+len(filesOpenTag) : j+k])
			body = strings.TrimSpace(body[:j] + body[j+k+len(filesCloseTag):])
		}
	}
	return body, files, true
}

// MergeFileOps adds src into dst.
func MergeFileOps(dst, src map[string]FileOp) { mergeFileOps(dst, src) }
