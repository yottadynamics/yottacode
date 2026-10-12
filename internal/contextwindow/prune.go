package contextwindow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

const (
	// PruneProtectTokens is the volume of the newest tool output that is
	// never pruned, so the model keeps the results it is actively working
	// from.
	PruneProtectTokens = 40_000
	// PruneMinSavingsTokens is the least total saving worth a prune. Pruning
	// rewrites old messages, which invalidates the provider's prompt-cache
	// prefix from the first changed message onward; requiring a large saving
	// amortizes that one-off cache miss.
	PruneMinSavingsTokens = 20_000
	// PruneMinResultTokens: results smaller than this aren't worth blanking
	// because the placeholder itself costs a handful of tokens.
	PruneMinResultTokens = 50

	prunedMarkerPrefix = "[Output pruned"
)

// IsPrunedPlaceholder reports whether content is a placeholder left by
// PruneToolOutputs.
func IsPrunedPlaceholder(content string) bool {
	return strings.HasPrefix(content, prunedMarkerPrefix)
}

// readerTools are tools whose result is (a window of) the content of the file
// named by their "path" argument.
var readerTools = map[string]bool{"read_file": true}

// pruneExemptTools never have their output pruned: their results are
// instructions or state the model must keep (skill bodies, plan/todo state).
var pruneExemptTools = map[string]bool{
	"Skill": true, "todo_write": true, "enter_plan_mode": true, "exit_plan_mode": true,
}

// PruneExempt reports whether a tool's output is never pruned. Exported so
// the agent package can assert its tool names haven't drifted from the list.
func PruneExempt(tool string) bool { return pruneExemptTools[tool] }

// defaultReadLines mirrors read_file's default window; a read with no
// offset/limit returns at most this many lines.
const defaultReadLines = 2000

// readKey identifies one read window of a file.
type readKey struct {
	path          string
	offset, limit int
}

func (k readKey) fromStart() bool { return k.offset <= 1 }
func (k readKey) isFull() bool    { return k.fromStart() && k.limit <= 0 }

// covers reports whether a read newer than old returned at least the same
// lines, so old is redundant: an identical window, or an unbounded read from
// the start when old began at the start and fit within the default window.
func (k readKey) covers(old readKey) bool {
	if k.path != old.path {
		return false
	}
	if k == old || (k.offset <= 1 && old.offset <= 1 && k.limit == old.limit) {
		return true
	}
	return k.isFull() && old.fromStart() && old.limit <= defaultReadLines
}

// PruneToolOutputs blanks old tool results in place (call/result pairing is
// untouched; only Content changes) and returns the rewritten slice, the
// estimated tokens saved, and whether anything changed. The input is never
// mutated.
//
// Two classes are pruned:
//   - superseded reads: a read_file whose path is read again later, regardless
//     of size above PruneMinResultTokens;
//   - stale bulk: tool results older than the newest PruneProtectTokens of
//     tool output.
//
// Nothing is changed unless the total saving reaches PruneMinSavingsTokens.
func PruneToolOutputs(msgs []adapter.Message) ([]adapter.Message, int, bool) {
	callName := map[string]string{}
	callRead := map[string]readKey{}
	for _, m := range msgs {
		if m.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			callName[tc.ID] = tc.Name
			if readerTools[tc.Name] {
				if k, ok := parseReadKey(tc.ArgsJSON); ok {
					callRead[tc.ID] = k
				}
			}
		}
	}

	// Newest-first walk: the first PruneProtectTokens of tool output are
	// protected; a path seen again going backwards marks the older read stale.
	prune := map[int]bool{}
	var newerReads []readKey
	protected := 0
	saved := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != adapter.RoleTool || IsPrunedPlaceholder(m.Content) {
			continue
		}
		if pruneExemptTools[callName[m.ToolCallID]] || strings.HasPrefix(strings.TrimSpace(m.Content), "error:") {
			continue
		}
		tok := EstimateMessage(m)
		superseded := false
		if k, ok := callRead[m.ToolCallID]; ok {
			for _, n := range newerReads {
				if n.covers(k) {
					superseded = true
					break
				}
			}
			newerReads = append(newerReads, k)
		}
		if protected < PruneProtectTokens && !superseded {
			protected += tok
			continue
		}
		if tok < PruneMinResultTokens {
			continue
		}
		prune[i] = true
		saved += tok - EstimateText(placeholder(callName[m.ToolCallID], tok))
	}
	if len(prune) == 0 || saved < PruneMinSavingsTokens {
		return msgs, 0, false
	}
	out := make([]adapter.Message, len(msgs))
	copy(out, msgs)
	for i := range prune {
		m := out[i]
		m.Content = placeholder(callName[m.ToolCallID], EstimateMessage(m))
		m.Images = nil
		out[i] = m
	}
	return out, saved, true
}

func placeholder(tool string, tokens int) string {
	if tool == "" {
		return fmt.Sprintf("%s - %d tokens]", prunedMarkerPrefix, tokens)
	}
	return fmt.Sprintf("%s: %s - %d tokens]", prunedMarkerPrefix, tool, tokens)
}

func parseReadKey(argsJSON string) (readKey, bool) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if json.Unmarshal([]byte(argsJSON), &a) != nil || a.Path == "" {
		return readKey{}, false
	}
	return readKey{path: a.Path, offset: a.Offset, limit: a.Limit}, true
}
