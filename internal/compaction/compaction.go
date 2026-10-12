// Package compaction is the summarization engine shared by the in-loop
// (internal/agent, mid-turn) and turn-boundary (internal/tui, /summarize and
// auto-summarize) context-compaction paths: one prompt family, one summary
// format, iterative updates, tool-call-aware input rendering, and a
// deterministic file list. Each caller keeps its own message layout; only the
// engine is shared. It imports neither agent nor tui.
package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// Streamer is the slice of a chat adapter the summarizer needs.
type Streamer interface {
	ChatStream(ctx context.Context, messages []adapter.Message, tools []adapter.Tool) <-chan adapter.StreamEvent
}

// Retained-tail tool-result caps shared by both paths: any one retained tool
// message is capped at MaxRetainedToolTokens, and when many compete for a
// budget none shrinks below MinRetainedToolTokens.
const (
	MaxRetainedToolTokens = 4096
	MinRetainedToolTokens = 256
	// RetainedToolMarker is appended to retained tool content cut by a budget.
	RetainedToolMarker = "…(truncated by compaction)"
)

var (
	ErrEmptyBody    = errors.New("compaction: nothing to summarize")
	ErrEmptySummary = errors.New("compaction: model returned empty summary")
)

// SectionHeaders are the sections every summary carries, in order.
var SectionHeaders = []string{
	"## Goal and progress",
	"## Decisions made",
	"## Code changes",
	"## Open questions",
	"## Preferences expressed",
	"## Next step",
}

const sectionSpec = `Produce EXACTLY these six sections, in this order, even if a section is empty (write "(none)" in that case):

## Goal and progress
The task/goal as currently understood; key findings, facts, file paths and tool results discovered so far.

## Decisions made
Finalized choices, and approaches already ruled out.

## Code changes
Files touched, functions added or removed, patterns established.

## Open questions
Unresolved items.

## Preferences expressed
Style, tooling, or workflow preferences the user stated.

## Next step
What remains to be done and the immediate next step.

Be concrete and terse. Do not invent. Omit pleasantries, restatements of these instructions, and exploratory tangents.`

// Prompt is the system prompt for a first compaction.
const Prompt = `You compact the working history of an in-progress coding-agent session so it can continue with bounded context. A capable agent must be able to resume seamlessly without re-reading the dropped messages.

` + sectionSpec

// UpdatePrompt is used when an earlier compaction already produced a summary:
// the model merges the new activity into it instead of summarizing a summary.
const UpdatePrompt = `You are updating the summary of an in-progress coding-agent session. The <previous-summary> is the existing summary; the <conversation> is what happened since. Produce ONE updated summary that replaces the previous one, so a capable agent could resume seamlessly.

Keep still-relevant facts from the previous summary, drop what is superseded or completed, and fold in the new activity.

` + sectionSpec

// EnsureSections appends any missing section header (with "(none)") so
// downstream code can rely on the structure. The model is told to emit all
// six; this absorbs drift without rejecting an otherwise-useful summary.
func EnsureSections(s string) string {
	out := s
	for _, h := range SectionHeaders {
		if !strings.Contains(out, h) {
			out += "\n\n" + h + "\n(none)"
		}
	}
	return strings.TrimSpace(out)
}

// Input is one summarization request.
type Input struct {
	// Middle is the history being dropped.
	Middle []adapter.Message
	// Window is the context window to budget the request against (the smaller
	// of the loop's and the summarizer's). <=0 disables budgeting.
	Window int
	// Scale is the learned estimate-to-provider correction ratio. Values above
	// one shrink the rendered input budget to protect the summarizer request.
	Scale float64
	// Prev is the previous summary prose (no <files> block), "" for a first
	// compaction. PrevFiles is the file set that note recorded.
	Prev      string
	PrevFiles map[string]FileOp
}

// Summarize runs one summary completion over in.Middle and returns the
// structured summary followed by a deterministic <files> block (files from the
// dropped tool calls merged with in.PrevFiles). onFallback, when non-nil,
// receives router fallback events seen while streaming.
func Summarize(ctx context.Context, s Streamer, in Input, onFallback func(adapter.StreamEvent)) (string, error) {
	prompt := Prompt
	budgetWindow := in.Window
	if in.Prev != "" {
		prompt = UpdatePrompt
		// The previous summary rides in the same request; shrink the window
		// the conversation is budgeted against by its cost, but never below a
		// quarter of the window so a huge note can't zero the body.
		budgetWindow = max(in.Window-contextwindow.EstimateText(in.Prev), in.Window/4)
	}
	if in.Scale > 1 && budgetWindow > 0 {
		budgetWindow = int(float64(budgetWindow) / in.Scale)
	}
	body := Render(in.Middle, budgetWindow)
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	if in.Prev != "" {
		body = "<previous-summary>\n" + in.Prev + "\n</previous-summary>\n\n<conversation>\n" + body + "\n</conversation>"
	}
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: prompt},
		{Role: adapter.RoleUser, Content: body},
	}
	var b strings.Builder
	for ev := range s.ChatStream(ctx, msgs, nil) {
		switch ev.Kind {
		case adapter.EventTokenDelta:
			b.WriteString(ev.Token)
		case adapter.EventFallback:
			if onFallback != nil {
				onFallback(ev)
			}
		case adapter.EventErr:
			return "", ev.Err
		case adapter.EventDone:
			if b.Len() == 0 && ev.Final != nil {
				b.WriteString(ev.Final.Content)
			}
		}
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		return "", ErrEmptySummary
	}
	summary = EnsureSections(summary)
	files := ExtractFileOps(in.Middle)
	MergeFileOps(files, in.PrevFiles)
	if block := FormatFilesBlock(files); block != "" {
		summary += "\n\n" + block
	}
	return summary, nil
}

// maxRenderedArgChars bounds the tool-call arguments shown to the summarizer;
// enough to identify a path, pattern, or command without letting a large
// write_file payload dominate the input.
const maxRenderedArgChars = 200

// compactArgs renders a tool call's argument JSON as " (args)", truncated.
func compactArgs(argsJSON string) string {
	a := strings.TrimSpace(argsJSON)
	if a == "" || a == "{}" {
		return ""
	}
	if len(a) > maxRenderedArgChars {
		a = a[:maxRenderedArgChars] + "…"
	}
	return " (" + a + ")"
}

// Render flattens msgs into one text body, keeping the most recent segments
// that fit a budget derived from window (leaving room for the prompt and the
// reserved output). Oldest segments are dropped first with a marker.
func Render(msgs []adapter.Message, window int) string {
	segs := make([]string, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case adapter.RoleUser:
			segs = append(segs, "USER: "+strings.TrimSpace(m.Content)+"\n\n")
		case adapter.RoleAssistant:
			var sb strings.Builder
			if c := strings.TrimSpace(m.Content); c != "" {
				sb.WriteString("ASSISTANT: ")
				sb.WriteString(c)
				sb.WriteString("\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "ASSISTANT used tool %s%s\n", tc.Name, compactArgs(tc.ArgsJSON))
			}
			if sb.Len() > 0 {
				segs = append(segs, sb.String())
			}
		case adapter.RoleTool:
			out := strings.TrimSpace(m.Content)
			if len(out) > 600 {
				out = out[:600] + "…(truncated)"
			}
			segs = append(segs, "TOOL_RESULT: "+out+"\n\n")
		}
	}

	if window <= 0 {
		return strings.TrimSpace(strings.Join(segs, ""))
	}
	budget := InputBudget(window)
	if budget <= 0 {
		// The window is too small for the standard output reservation (e.g.
		// an ~8K-window summarizer). Returning everything would send an
		// over-window request and fail; fall back to a conservative fraction
		// so a tiny window degrades to a lossy summary instead.
		budget = window / 2
	}
	used := 0
	keepFrom := len(segs)
	for i := len(segs) - 1; i >= 0; i-- {
		cost := (len(segs[i]) + 3) / 4
		if used+cost > budget && keepFrom < len(segs) {
			break
		}
		used += cost
		keepFrom = i
	}
	if keepFrom == 0 {
		return strings.TrimSpace(strings.Join(segs, ""))
	}
	var b strings.Builder
	b.WriteString("[earlier turns omitted due to compaction budget]\n\n")
	for i := keepFrom; i < len(segs); i++ {
		b.WriteString(segs[i])
	}
	return strings.TrimSpace(b.String())
}

// summarizeInputSafety is held back beyond the prompt and output reservation
// for request-envelope overhead (role wrappers, JSON framing).
const summarizeInputSafety = 2048

// InputBudget is the token budget for the flattened transcript. scale is an
// optional estimate-to-provider correction ratio; values above 1 shrink the
// rendered body so the summary request remains inside the real window.
func InputBudget(window int, scale ...float64) int {
	overhead := (max(len(Prompt), len(UpdatePrompt)) + 3) / 4
	budget := window - overhead - int(adapter.ChatDefaultMaxTokens) - summarizeInputSafety
	factor := 1.0
	if len(scale) > 0 && scale[0] > 0 {
		factor = scale[0]
	}
	return int(float64(budget) / factor)
}
