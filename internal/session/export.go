package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// ExportMarkdown serializes a Session into a human-readable markdown
// document — useful for sharing a working session in a PR description,
// an issue, or just archiving. We deliberately omit the system message
// (boilerplate plus injected memory, not interesting to a reader) and
// fence tool output for clarity.
//
// Living in the session package (rather than internal/tui) lets the
// non-interactive cobra subcommands call it without dragging the
// bubbletea/lipgloss dependency tree into the CLI binary's
// non-interactive paths.
func ExportMarkdown(s *Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# yottacode session\n\n")
	fmt.Fprintf(&b, "- **id:** %s\n", s.ID)
	if s.Name != "" {
		fmt.Fprintf(&b, "- **name:** %s\n", s.Name)
	}
	fmt.Fprintf(&b, "- **model:** `%s`\n", s.Model)
	fmt.Fprintf(&b, "- **created:** %s\n", s.Created.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **cwd:** `%s`\n", s.Cwd)
	b.WriteString("\n---\n\n")

	for _, msg := range s.Messages {
		switch msg.Role {
		case adapter.RoleSystem:
			// Skipped: the base prompt + injected memory is internal context.
		case adapter.RoleUser:
			fmt.Fprintf(&b, "## User\n\n%s\n\n", strings.TrimRight(msg.Content, "\n"))
		case adapter.RoleAssistant:
			b.WriteString("## Assistant\n\n")
			if msg.Content != "" {
				fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(msg.Content, "\n"))
			}
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&b, "**Tool call:** `%s(%s)`\n\n", tc.Name, tc.ArgsJSON)
			}
			if len(msg.Citations) > 0 {
				b.WriteString("**Sources**\n\n")
				for _, c := range msg.Citations {
					if label := citationLabel(c); label != "" {
						fmt.Fprintf(&b, "- %s\n", label)
					}
				}
				b.WriteString("\n")
			}
		case adapter.RoleTool:
			fmt.Fprintf(&b, "**Tool output**\n\n```\n%s\n```\n\n",
				strings.TrimRight(msg.Content, "\n"))
		}
	}
	return b.String()
}

// repeatedToolFailureMarker mirrors agent.RepeatedToolFailureMarker without
// importing the agent package into session export code; JSONL only needs to
// classify persisted text that already contains this marker.
const repeatedToolFailureMarker = "repeated tool failure ("

// jsonlHeader is the first line of a session's JSONL export — the file's
// own metadata, so it's self-describing without needing the filename.
type jsonlHeader struct {
	Type    string    `json:"type"`
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Model   string    `json:"model"`
	Cwd     string    `json:"cwd"`
	Created time.Time `json:"created"`
}

// jsonlEvent is one line of a session's JSONL export: a user message, an
// assistant message, a tool call, or a tool result. Fields that don't
// apply to a given Type are left zero and omitted (omitempty), so a
// consumer scanning with jq sees only the fields that actually apply to
// that line instead of a wall of nulls.
type jsonlEvent struct {
	Timestamp  *time.Time      `json:"ts,omitempty"`
	Turn       int             `json:"turn,omitempty"`
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"` // tool_call/tool_result correlation id
	Tool       string          `json:"tool,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Status     string          `json:"status,omitempty"`
	Content    string          `json:"content,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *adapter.Usage  `json:"usage,omitempty"`
	// Model/Provider: assistant lines only — which model/provider
	// actually produced this turn (see adapter.Message.Model's doc
	// comment: can differ from the session's headline model under
	// fallback or a mid-session /model switch).
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// LatencyMS/TTFTMs: assistant lines only.
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	TTFTMs    *int64 `json:"ttft_ms,omitempty"`
	// FallbackCount/FallbackReason: assistant lines only — set when
	// adapter.MultiStreamer fell through to a different candidate before
	// this turn's call succeeded.
	FallbackCount  int    `json:"fallback_count,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	// ApprovalSource: tool_result lines only — how the call got
	// permission to run (see adapter.Message.ApprovalSource's doc
	// comment for the possible values).
	ApprovalSource string `json:"approval_source,omitempty"`
}

// jsonlCompaction is one compaction event line, sourced from
// Session.CompactionEvents (already persisted for every /summarize or
// auto-summarize firing). Emitted as its own block right after the
// header rather than interleaved by position in the per-message walk —
// compaction rewrites history in place rather than adding a new message,
// so there's no natural message-sequence slot for it. Each still carries
// its own real timestamp, so a consumer who wants strict chronological
// order across the whole file (messages included) can sort by ts.
type jsonlCompaction struct {
	Type   string    `json:"type"`
	At     time.Time `json:"ts"`
	Before int       `json:"before"`
	After  int       `json:"after"`
	Auto   bool      `json:"auto,omitempty"`
}

// jsonlSummary is the final line of a session's JSONL export: totals
// computed over the whole session — the same aggregates /usage renders
// live, folded once into the log so a consumer doesn't have to re-derive
// them by scanning every line. SubagentCount/SubagentUsage are omitted
// entirely when the session ran no subagents, rather than shown as zero.
type jsonlSummary struct {
	Type           string         `json:"type"`
	Turns          int            `json:"turns"`
	ToolCalls      int            `json:"tool_calls"`
	Usage          adapter.Usage  `json:"usage"`
	SubagentCount  int            `json:"subagent_count,omitempty"`
	SubagentUsage  *adapter.Usage `json:"subagent_usage,omitempty"`
	Compactions    int            `json:"compactions,omitempty"`
	FirstTimestamp *time.Time     `json:"first_ts,omitempty"`
	LastTimestamp  *time.Time     `json:"last_ts,omitempty"`
}

// ExportJSONL serializes a Session into newline-delimited JSON: a
// session-metadata header line, then one line per user message, assistant
// message, tool call, and tool result — meant to be grepped, diffed, and
// piped through jq, not read top to bottom. It complements ExportMarkdown
// rather than replacing it: the markdown export stays the narrative
// document for pasting into a PR or issue; this is the session's log.
//
// Deliberately full-fidelity — no truncation. /inspect's scan view trims
// aggressively because it renders in a fixed-height terminal popup; an
// export file has no such constraint, and truncating it would just make
// it a worse log.
//
// Turn numbers match /inspect's own scheme (each RoleAssistant message
// starts a new turn) so the two views cross-reference directly: a tool
// call's turn is the assistant message that issued it, and its matching
// tool_result inherits that same turn via ToolCallID — mirroring
// buildInspectTurns's callID->turn tracking in internal/tui/cmd_inspect.go
// (duplicated here rather than shared, since that package pulls in the
// bubbletea/lipgloss dependency tree this one deliberately avoids — see
// ExportMarkdown's doc comment).
func ExportJSONL(s *Session) string {
	var b strings.Builder
	writeLine := func(v any) {
		enc, err := json.Marshal(v)
		if err != nil {
			return // v is always one of this function's own structs.
		}
		b.Write(enc)
		b.WriteByte('\n')
	}

	writeLine(jsonlHeader{
		Type:    "session",
		ID:      s.ID,
		Name:    s.Name,
		Model:   s.Model,
		Cwd:     s.Cwd,
		Created: s.Created,
	})

	for _, c := range s.CompactionEvents {
		writeLine(jsonlCompaction{
			Type:   "compaction",
			At:     c.At,
			Before: c.Before,
			After:  c.After,
			Auto:   c.Auto,
		})
	}

	turnNum := 0
	toolCallCount := 0
	var totalUsage adapter.Usage
	var firstTs, lastTs *time.Time
	turnOfCall := map[string]int{}
	for _, msg := range s.Messages {
		if msg.Timestamp != nil {
			if firstTs == nil {
				firstTs = msg.Timestamp
			}
			lastTs = msg.Timestamp
		}
		switch msg.Role {
		case adapter.RoleSystem:
			// Skipped: same reasoning as ExportMarkdown — internal context,
			// not part of what happened in the session.
		case adapter.RoleUser:
			writeLine(jsonlEvent{
				Timestamp: msg.Timestamp,
				Turn:      turnNum + 1,
				Type:      "user",
				Content:   msg.Content,
			})
		case adapter.RoleAssistant:
			turnNum++
			if msg.Usage != nil {
				totalUsage.Add(msg.Usage)
			}
			writeLine(jsonlEvent{
				Timestamp:      msg.Timestamp,
				Turn:           turnNum,
				Type:           "assistant",
				Content:        msg.Content,
				StopReason:     msg.StopReason,
				Usage:          msg.Usage,
				Model:          msg.Model,
				Provider:       msg.Provider,
				LatencyMS:      msg.LatencyMS,
				TTFTMs:         msg.TTFTMs,
				FallbackCount:  msg.FallbackCount,
				FallbackReason: msg.FallbackReason,
			})
			for _, tc := range msg.ToolCalls {
				turnOfCall[tc.ID] = turnNum
				toolCallCount++
				writeLine(jsonlEvent{
					Timestamp: msg.Timestamp,
					Turn:      turnNum,
					Type:      "tool_call",
					ID:        tc.ID,
					Tool:      tc.Name,
					Args:      argsRawJSON(tc.ArgsJSON),
				})
			}
		case adapter.RoleTool:
			status := "ok"
			if strings.HasPrefix(msg.Content, "error:") {
				status = "error"
				if strings.Contains(msg.Content, repeatedToolFailureMarker) {
					status = "error — guidance fired"
				}
			}
			writeLine(jsonlEvent{
				Timestamp:      msg.Timestamp,
				Turn:           turnOfCall[msg.ToolCallID],
				Type:           "tool_result",
				ID:             msg.ToolCallID,
				Status:         status,
				Content:        msg.Content,
				ApprovalSource: msg.ApprovalSource,
			})
		}
	}

	summary := jsonlSummary{
		Type:           "session_summary",
		Turns:          turnNum,
		ToolCalls:      toolCallCount,
		Usage:          totalUsage,
		Compactions:    len(s.CompactionEvents),
		FirstTimestamp: firstTs,
		LastTimestamp:  lastTs,
	}
	if subUsage := s.SubagentUsage(); subUsage.AgentCount > 0 {
		summary.SubagentCount = subUsage.AgentCount
		summary.SubagentUsage = &subUsage.Total
	}
	writeLine(summary)

	return b.String()
}

// argsRawJSON embeds a tool call's ArgsJSON as a nested JSON object when
// it's actually valid JSON (the normal case) so a log consumer can query
// into it with jq, rather than getting back an escaped string. Falls back
// to a plain JSON string when it isn't valid — a model can in principle
// produce malformed args — so one bad tool call can't corrupt the line.
func argsRawJSON(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// citationLabel renders one Citation into the short form that appears
// under "**Sources**" in the export. Mirrors the TUI's render-time
// helper of the same name (kept private in each package — they may
// diverge if the TUI ever adds inline styling).
func citationLabel(c adapter.Citation) string {
	switch {
	case c.Title != "" && c.URL != "":
		return c.Title + " (" + c.URL + ")"
	case c.Title != "":
		return c.Title
	case c.Filename != "" && c.FileID != "":
		return c.Filename + " (" + c.FileID + ")"
	case c.Filename != "":
		return c.Filename
	case c.URL != "":
		return c.URL
	case c.FileID != "":
		return c.FileID
	default:
		return ""
	}
}
