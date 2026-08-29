package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func TestExport_ContainsHeaderMetadata(t *testing.T) {
	s := &Session{
		ID:      "test-id",
		Name:    "demo",
		Model:   "qwen3.5",
		Created: time.Date(2026, 4, 25, 6, 30, 0, 0, time.UTC),
		Cwd:     "/tmp/x",
	}
	got := ExportMarkdown(s)
	for _, want := range []string{"test-id", "demo", "qwen3.5", "2026-04-25", "/tmp/x"} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
}

func TestExport_ContainsConversationTurns(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleSystem, Content: "you are helpful"},
			{Role: adapter.RoleUser, Content: "hello"},
			{Role: adapter.RoleAssistant, Content: "world"},
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{ID: "c1", Name: "list_dir", ArgsJSON: `{"path":"."}`},
			}},
			{Role: adapter.RoleTool, Content: "f\tmain.go"},
		},
	}
	got := ExportMarkdown(s)

	// User + assistant content rendered.
	if !strings.Contains(got, "## User") || !strings.Contains(got, "hello") {
		t.Errorf("user turn missing")
	}
	if !strings.Contains(got, "## Assistant") || !strings.Contains(got, "world") {
		t.Errorf("assistant content missing")
	}
	// System message must be omitted (boilerplate).
	if strings.Contains(got, "you are helpful") {
		t.Errorf("system message should be omitted from export:\n%s", got)
	}
	// Tool call signaled.
	if !strings.Contains(got, "list_dir") {
		t.Errorf("tool call name missing")
	}
	// Tool result fenced.
	if !strings.Contains(got, "Tool output") || !strings.Contains(got, "main.go") {
		t.Errorf("tool output missing")
	}
}

func TestExport_IncludesAssistantSources(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{
				Role:    adapter.RoleAssistant,
				Content: "answer",
				Citations: []adapter.Citation{
					{Type: "url_citation", Title: "Example", URL: "https://example.com"},
					{Type: "file_citation", Filename: "notes.txt", FileID: "file_123"},
				},
			},
		},
	}
	got := ExportMarkdown(s)
	for _, want := range []string{"**Sources**", "Example (https://example.com)", "notes.txt (file_123)"} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
}

// jsonlLines parses ExportJSONL's output into individual decoded lines,
// failing the test immediately if any line isn't valid JSON — the whole
// point of the format is that every line parses on its own.
func jsonlLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return nil
	}
	var out []map[string]any
	for i, line := range strings.Split(raw, "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}
		out = append(out, m)
	}
	return out
}

func TestExportJSONL_HeaderLineFirst(t *testing.T) {
	s := &Session{
		ID:      "test-id",
		Name:    "demo",
		Model:   "qwen3.5",
		Created: time.Date(2026, 4, 25, 6, 30, 0, 0, time.UTC),
		Cwd:     "/tmp/x",
	}
	lines := jsonlLines(t, ExportJSONL(s))
	// Header + the always-present session_summary trailer, even for an
	// empty session (all-zero totals, not omitted — see TestExportJSONL_SummaryLine).
	if len(lines) != 2 {
		t.Fatalf("expected header + summary for an empty session, got %d lines", len(lines))
	}
	h := lines[0]
	if h["type"] != "session" || h["id"] != "test-id" || h["name"] != "demo" || h["model"] != "qwen3.5" || h["cwd"] != "/tmp/x" {
		t.Errorf("header line wrong: %+v", h)
	}
	if h["schema_version"] != float64(1) || !strings.HasPrefix(h["created"].(string), "2026-04-25") {
		t.Errorf("header schema_version/created wrong: schema_version=%v created=%v", h["schema_version"], h["created"])
	}
}

// TestExportJSONL_TurnNumberingMatchesInspect locks the cross-reference
// guarantee: a tool call's turn is the assistant message that issued it,
// and its tool_result inherits that same turn via id — the same scheme
// buildInspectTurns uses in internal/tui/cmd_inspect.go, so turn numbers
// printed by /inspect and turn numbers in the exported log agree.
func TestExportJSONL_TurnNumberingMatchesInspect(t *testing.T) {
	ts := time.Date(2026, 8, 27, 16, 52, 41, 0, time.UTC)
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleSystem, Content: "sys prompt"},
			{Role: adapter.RoleUser, Content: "check the build", Timestamp: &ts},
			{Role: adapter.RoleAssistant, Content: "on it", Timestamp: &ts, StopReason: "end_turn",
				Usage: &adapter.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 80},
				ToolCalls: []adapter.ToolCall{
					{ID: "call1", Name: "run_tests", ArgsJSON: `{"path":"."}`},
				}},
			{Role: adapter.RoleTool, ToolCallID: "call1", Content: "ok: passed", Timestamp: &ts},
			{Role: adapter.RoleUser, Content: "now lint"},
			{Role: adapter.RoleAssistant, Content: "done"},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))

	byType := map[string][]map[string]any{}
	for _, l := range lines {
		byType[l["type"].(string)] = append(byType[l["type"].(string)], l)
	}

	if strings.Contains(ExportJSONL(s), "sys prompt") {
		t.Error("system message should be omitted from the JSONL export, same as ExportMarkdown")
	}

	if len(byType["user"]) != 2 || byType["user"][0]["turn"].(float64) != 1 {
		t.Errorf("first user message should carry turn 1 (the turn it precedes), got %+v", byType["user"])
	}
	if len(byType["assistant"]) != 2 || byType["assistant"][0]["turn"].(float64) != 1 {
		t.Errorf("first assistant message should be turn 1, got %+v", byType["assistant"])
	}
	if byType["assistant"][0]["stop_reason"] != "end_turn" {
		t.Errorf("assistant stop_reason missing: %+v", byType["assistant"][0])
	}
	usage, ok := byType["assistant"][0]["usage"].(map[string]any)
	if !ok || usage["input_tokens"].(float64) != 100 || usage["cache_read_tokens"].(float64) != 80 {
		t.Errorf("assistant usage missing/wrong: %+v", byType["assistant"][0]["usage"])
	}

	call := byType["tool_call"][0]
	result := byType["tool_result"][0]
	if call["id"] != "call1" || result["id"] != "call1" {
		t.Errorf("tool_call/tool_result should share id=call1: call=%+v result=%+v", call, result)
	}
	if call["turn"].(float64) != 1 || result["turn"].(float64) != 1 {
		t.Errorf("tool_call and its tool_result should both land on turn 1: call=%v result=%v", call["turn"], result["turn"])
	}
	if args, ok := call["args"].(map[string]any); !ok || args["path"] != "." {
		t.Errorf("tool_call args should embed as a nested object, got %+v (%T)", call["args"], call["args"])
	}
	if result["status"] != "ok" {
		t.Errorf("tool_result status = %v, want ok", result["status"])
	}
	if call["ts"] == nil || result["ts"] == nil {
		t.Errorf("timestamped messages should carry ts: call=%v result=%v", call["ts"], result["ts"])
	}

	if len(byType["user"]) > 1 && byType["user"][1]["turn"].(float64) != 2 {
		t.Errorf("second user message should carry turn 2, got %+v", byType["user"][1])
	}
	if byType["user"][0]["ts"] == nil {
		t.Error("timestamped user message should carry ts")
	}
	if byType["assistant"][1]["ts"] != nil {
		t.Error("an untimestamped message (old session) should omit ts, not emit a zero value")
	}
}

// TestExportJSONL_ErrorStatusAndGuidanceMarker mirrors buildInspectTurns's
// status computation, so /inspect and the exported log agree on which
// calls failed and which tripped the repeated-failure guard.
func TestExportJSONL_ErrorStatusAndGuidanceMarker(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{ID: "c1", Name: "grep", ArgsJSON: `{}`},
				{ID: "c2", Name: "lint", ArgsJSON: `{}`},
			}},
			{Role: adapter.RoleTool, ToolCallID: "c1", Content: "error: hunk mismatch\n\n" + repeatedToolFailureMarker + "3×): rebuild a valid unified diff"},
			{Role: adapter.RoleTool, ToolCallID: "c2", Content: "error: 2 warnings"},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	var results []map[string]any
	for _, l := range lines {
		if l["type"] == "tool_result" {
			results = append(results, l)
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result lines, got %d", len(results))
	}
	if results[0]["status"] != "error — guidance fired" {
		t.Errorf("c1 status = %v, want %q", results[0]["status"], "error — guidance fired")
	}
	if results[1]["status"] != "error" {
		t.Errorf("c2 status = %v, want %q", results[1]["status"], "error")
	}
	// Full fidelity, unlike /inspect's truncated preview: the raw content
	// (including the guard-marker boilerplate) is preserved as-is.
	if !strings.Contains(results[0]["content"].(string), repeatedToolFailureMarker) {
		t.Errorf("expected the full untruncated content, including the guard marker, got %v", results[0]["content"])
	}
}

// TestExportJSONL_MalformedArgsFallBackToString confirms one tool call
// with invalid JSON args can't corrupt its own line — args falls back to
// a quoted string instead of raw (invalid) embedded JSON.
func TestExportJSONL_MalformedArgsFallBackToString(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{ID: "c1", Name: "weird", ArgsJSON: `{not valid json`},
			}},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s)) // fails the test if any line doesn't parse
	var call map[string]any
	for _, l := range lines {
		if l["type"] == "tool_call" {
			call = l
		}
	}
	if call["args"] != `{not valid json` {
		t.Errorf("args = %v, want the raw string preserved", call["args"])
	}
}

// TestExportJSONL_AssistantLineCarriesNewFields confirms the fields
// added alongside cache/reasoning tokens — Model, Provider, LatencyMS,
// TTFTMs, FallbackCount/FallbackReason — actually reach the exported
// assistant line, not just the in-memory Message.
func TestExportJSONL_AssistantLineCarriesNewFields(t *testing.T) {
	latency := int64(1840)
	ttft := int64(220)
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "hi"},
			{
				Role:           adapter.RoleAssistant,
				Content:        "hello",
				Model:          "claude-sonnet-5",
				Provider:       "anthropic",
				LatencyMS:      &latency,
				TTFTMs:         &ttft,
				FallbackCount:  1,
				FallbackReason: "rate limited",
			},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	var assistant map[string]any
	for _, l := range lines {
		if l["type"] == "assistant" {
			assistant = l
		}
	}
	if assistant == nil {
		t.Fatal("no assistant line found")
	}
	checks := map[string]any{
		"model": "claude-sonnet-5", "provider": "anthropic",
		"latency_ms": float64(1840), "ttft_ms": float64(220),
		"fallback_count": float64(1), "fallback_reason": "rate limited",
	}
	for k, want := range checks {
		if got := assistant[k]; got != want {
			t.Errorf("assistant[%q] = %v, want %v", k, got, want)
		}
	}
}

// TestExportJSONL_ToolResultCarriesApprovalSource confirms the approval
// source lands on the tool_result line so a log can answer "how did this
// call get permission to run."
func TestExportJSONL_ToolResultCarriesApprovalSource(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "run_bash", ArgsJSON: `{}`}}},
			{Role: adapter.RoleTool, ToolCallID: "c1", Content: "ok", ApprovalSource: "yolo-mode"},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	var result map[string]any
	for _, l := range lines {
		if l["type"] == "tool_result" {
			result = l
		}
	}
	if result == nil || result["approval_source"] != "yolo-mode" {
		t.Errorf("tool_result approval_source = %v, want %q", result["approval_source"], "yolo-mode")
	}
}

// TestExportJSONL_ToolResultCarriesImageMetadata confirms multimodal tool
// results are visible in the activity log without embedding raw binary bytes.
func TestExportJSONL_ToolResultCarriesImageMetadata(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "read_file", ArgsJSON: `{"path":"shot.png"}`}}},
			{Role: adapter.RoleTool, ToolCallID: "c1", Content: "[image: shot.png]", Images: []adapter.ImageBlock{{MediaType: "image/png", Data: []byte("png-bytes")}}},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	var result map[string]any
	for _, l := range lines {
		if l["type"] == "tool_result" {
			result = l
		}
	}
	images, ok := result["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("tool_result images = %#v, want one metadata entry", result["images"])
	}
	img := images[0].(map[string]any)
	if img["media_type"] != "image/png" || img["bytes"] != float64(len("png-bytes")) {
		t.Errorf("image metadata wrong: %+v", img)
	}
	if sha, ok := img["sha256"].(string); !ok || len(sha) != 64 {
		t.Errorf("image sha256 = %v, want 64-char hex digest", img["sha256"])
	}
	if strings.Contains(ExportJSONL(s), "png-bytes") {
		t.Error("JSONL activity log should not embed raw image bytes")
	}
}

// TestExportJSONL_CompactionEvents confirms Session.CompactionEvents —
// already persisted for every /summarize or auto-summarize firing —
// show up as their own lines, right after the header.
func TestExportJSONL_CompactionEvents(t *testing.T) {
	at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		CompactionEvents: []CompactionRecord{
			{At: at, Before: 150000, After: 20000, Auto: true},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	if len(lines) < 2 || lines[1]["type"] != "compaction" {
		t.Fatalf("expected a compaction line right after the header, got: %+v", lines)
	}
	c := lines[1]
	if c["before"] != float64(150000) || c["after"] != float64(20000) || c["auto"] != true {
		t.Errorf("compaction line wrong: %+v", c)
	}
	if !strings.HasPrefix(c["ts"].(string), "2026-08-27T12:00:00Z") {
		t.Errorf("compaction ts = %v, want 2026-08-27T12:00:00Z", c["ts"])
	}
}

// TestExportJSONL_SummaryLine locks the trailer's shape: totals computed
// over the whole session, subagent fields present only when the session
// actually ran subagents (not shown as zero).
func TestExportJSONL_SummaryLine(t *testing.T) {
	t1 := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Second)
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "go", Timestamp: &t1},
			{Role: adapter.RoleAssistant, Content: "on it", Timestamp: &t1,
				Usage: &adapter.Usage{InputTokens: 100, OutputTokens: 20},
				ToolCalls: []adapter.ToolCall{
					{ID: "c1", Name: "run_bash", ArgsJSON: `{}`},
					{ID: "c2", Name: "read_file", ArgsJSON: `{}`},
				}},
			{Role: adapter.RoleTool, ToolCallID: "c1", Content: "ok"},
			{Role: adapter.RoleTool, ToolCallID: "c2", Content: "ok"},
			{Role: adapter.RoleAssistant, Content: "done", Timestamp: &t2,
				Usage: &adapter.Usage{InputTokens: 150, OutputTokens: 10}},
		},
		CompactionEvents: []CompactionRecord{{At: t1, Before: 1000, After: 100}},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	summary := lines[len(lines)-1]
	if summary["type"] != "session_summary" {
		t.Fatalf("last line should be session_summary, got %+v", summary)
	}
	if summary["turns"] != float64(2) {
		t.Errorf("turns = %v, want 2", summary["turns"])
	}
	if summary["tool_calls"] != float64(2) {
		t.Errorf("tool_calls = %v, want 2", summary["tool_calls"])
	}
	usage, ok := summary["usage"].(map[string]any)
	if !ok || usage["input_tokens"] != float64(250) || usage["output_tokens"] != float64(30) {
		t.Errorf("usage totals wrong: %+v", summary["usage"])
	}
	if summary["compactions"] != float64(1) {
		t.Errorf("compactions = %v, want 1", summary["compactions"])
	}
	if !strings.HasPrefix(summary["first_ts"].(string), "2026-08-27T12:00:00Z") {
		t.Errorf("first_ts = %v, want 2026-08-27T12:00:00Z", summary["first_ts"])
	}
	if !strings.HasPrefix(summary["last_ts"].(string), "2026-08-27T12:00:30Z") {
		t.Errorf("last_ts = %v, want 2026-08-27T12:00:30Z", summary["last_ts"])
	}
	if _, present := summary["subagent_count"]; present {
		t.Errorf("subagent_count should be omitted for a session with no subagents, got %v", summary["subagent_count"])
	}
}

// TestExportJSONL_SummaryLineIncludesSubagents confirms subagent
// count/usage appear when the session actually ran subagents — sourced
// from Session.SubagentUsage(), the same rollup /usage already computes.
func TestExportJSONL_SummaryLineIncludesSubagents(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		SubagentTasks: []subagents.TaskRecord{
			{ID: "t1", Status: subagents.TaskCompleted, Usage: adapter.Usage{InputTokens: 500, OutputTokens: 50}},
			{ID: "t2", Status: subagents.TaskCompleted, Usage: adapter.Usage{InputTokens: 300, OutputTokens: 30}},
		},
	}
	lines := jsonlLines(t, ExportJSONL(s))
	summary := lines[len(lines)-1]
	if summary["subagent_count"] != float64(2) {
		t.Errorf("subagent_count = %v, want 2", summary["subagent_count"])
	}
	subUsage, ok := summary["subagent_usage"].(map[string]any)
	if !ok || subUsage["input_tokens"] != float64(800) {
		t.Errorf("subagent_usage wrong: %+v", summary["subagent_usage"])
	}
}
