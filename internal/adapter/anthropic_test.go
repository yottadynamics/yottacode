package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewWithConfig_RoutesAnthropic verifies the router selects the
// Anthropic adapter both via base-URL detection and via model-tag
// detection. The tag-based path matters because corp gateways
// sometimes proxy api.anthropic.com behind a custom hostname.
func TestNewWithConfig_RoutesAnthropic(t *testing.T) {
	t.Run("by base url", func(t *testing.T) {
		c := NewWithConfig(Config{
			BaseURL: "https://api.anthropic.com",
			APIKey:  "k",
			Model:   "claude-sonnet-4-6",
		})
		if _, ok := c.(*anthropicAdapter); !ok {
			t.Fatalf("got %T, want *anthropicAdapter", c)
		}
	})
	t.Run("by model prefix on a custom proxy host", func(t *testing.T) {
		c := NewWithConfig(Config{
			BaseURL: "https://corp-gateway.example.com",
			APIKey:  "k",
			Model:   "claude-opus-4-7",
		})
		if _, ok := c.(*anthropicAdapter); !ok {
			t.Fatalf("got %T, want *anthropicAdapter", c)
		}
	})
}

// TestAnthropicAdapter_StreamsTextThinkingAndToolUse runs a streaming
// chat against a recorded SSE script — covering the three event types
// yottacode cares about (text, thinking, tool_use). Verifies:
//
//   - text_delta tokens land as EventTokenDelta
//   - thinking_delta tokens land as EventReasoning
//   - input_json_delta fragments accumulate into a single ToolCall
//     keyed on the tool_use block's id + name
//   - the final EventDone carries the assembled assistant Message
func TestAnthropicAdapter_StreamsTextThinkingAndToolUse(t *testing.T) {
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think… "}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking the file."}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Reading "}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"the file now."}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":1}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"read_file","input":{}}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":2}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-6",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools := []Tool{{Name: "read_file", Description: "read", Schema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []string{"path"},
	}}}

	stream := a.ChatStream(ctx, []Message{
		{Role: RoleSystem, Content: "you are helpful"},
		{Role: RoleUser, Content: "read main.go"},
	}, tools)

	var (
		text, reasoning strings.Builder
		final           *Message
		seenErr         error
	)
	for ev := range stream {
		switch ev.Kind {
		case EventTokenDelta:
			text.WriteString(ev.Token)
		case EventReasoning:
			reasoning.WriteString(ev.Token)
		case EventDone:
			final = ev.Final
		case EventErr:
			seenErr = ev.Err
		}
	}
	if seenErr != nil {
		t.Fatalf("stream errored: %v", seenErr)
	}
	if got := text.String(); got != "Reading the file now." {
		t.Errorf("text body = %q", got)
	}
	if got := reasoning.String(); got != "Let me think… checking the file." {
		t.Errorf("reasoning = %q", got)
	}
	if final == nil {
		t.Fatal("expected final EventDone")
	}
	if final.Role != RoleAssistant {
		t.Errorf("final.Role = %q", final.Role)
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(final.ToolCalls))
	}
	tc := final.ToolCalls[0]
	if tc.ID != "toolu_01" || tc.Name != "read_file" {
		t.Errorf("tool call meta = %+v", tc)
	}
	if tc.ArgsJSON != `{"path":"main.go"}` {
		t.Errorf("tool args = %q", tc.ArgsJSON)
	}
	if final.StopReason != "tool_use" {
		t.Errorf("stop reason = %q", final.StopReason)
	}
}

// TestAnthropicAdapter_TruncatedToolUseErrors guards the truncation fix:
// a tool_use block that starts and streams input but never receives its
// content_block_stop before the stream ends must surface an error rather
// than a clean final that silently drops the call.
func TestAnthropicAdapter_TruncatedToolUseErrors(t *testing.T) {
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"m","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"read_file","input":{}}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
			// stream ends mid-tool-call: no content_block_stop, no message_stop
		},
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4-6"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "read x"}}, []Tool{{
		Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"},
	}})

	var final *Message
	var seenErr error
	for ev := range stream {
		switch ev.Kind {
		case EventDone:
			final = ev.Final
		case EventErr:
			seenErr = ev.Err
		}
	}
	if seenErr == nil {
		t.Error("truncated tool_use should surface an error, not a clean final")
	}
	if final != nil && len(final.ToolCalls) > 0 {
		t.Errorf("must not emit a final carrying the incomplete tool call: %+v", final)
	}
}

// TestAnthropicAdapter_EmitsStreamProgressForToolUseArgs guards the
// "0.0 tok/s never moves" fix on the Anthropic side: when a Claude
// turn produces only a tool_use block (no text, no thinking), the
// input_json_delta stream is the sole signal of activity. The
// adapter surfaces each delta as an EventStreamProgress heartbeat so
// the TUI's tok/s indicator keeps climbing instead of sitting at 0.0
// the whole turn.
func TestAnthropicAdapter_EmitsStreamProgressForToolUseArgs(t *testing.T) {
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_p","role":"assistant","content":[],"model":"claude-opus-4-7"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_p","name":"read_file","input":{}}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"x.go\"}"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{BaseURL: srv.URL, APIKey: "k", Model: "claude-opus-4-7"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "read x"}}, []Tool{{
		Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"},
	}})

	var progress int
	for ev := range stream {
		if ev.Kind == EventStreamProgress {
			progress++
		}
	}
	if progress != 2 {
		t.Errorf("EventStreamProgress count = %d, want 2 (one per input_json_delta)", progress)
	}
}

// TestAnthropicAdapter_ContinuesAfterToolCall covers the round-trip
// shape: when prior history already contains an assistant turn with a
// ToolCall plus a RoleTool message carrying the result, we must
// translate that into:
//
//   - an assistant MessageParam with a tool_use ContentBlock (id + name
//     + input round-tripped back to a JSON object, NOT a stringified
//     args field)
//   - a user MessageParam with a tool_result ContentBlock keyed on the
//     same tool_use_id
//
// Anthropic 400s on a missing or mismatched tool_use_id, so this test
// is the regression guard for the second-turn flow that drove the
// whole "let users call Claude" effort. Captures the wire payload and
// asserts the block shapes directly.
func TestAnthropicAdapter_ContinuesAfterToolCall(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_3","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "claude-sonnet-4-6",
	})

	stream := a.ChatStream(context.Background(),
		[]Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "read main.go"},
			// Prior assistant turn issued a tool_call. Note the
			// stringified args — we expect the adapter to JSON-decode
			// that back into an object before sending.
			{
				Role:    RoleAssistant,
				Content: "Reading.",
				ToolCalls: []ToolCall{{
					ID:       "toolu_01",
					Name:     "read_file",
					ArgsJSON: `{"path":"main.go"}`,
				}},
			},
			// Tool result for that call.
			{
				Role:       RoleTool,
				Content:    "package main\n",
				ToolCallID: "toolu_01",
			},
		},
		nil,
	)
	for range stream {
	}

	body := <-captured
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant-with-tool-use, user-with-tool-result), got %v", body["messages"])
	}

	// Message 1: original user prompt
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("messages[0].role = %v, want user", first["role"])
	}

	// Message 2: assistant turn with text + tool_use blocks
	second := msgs[1].(map[string]any)
	if second["role"] != "assistant" {
		t.Errorf("messages[1].role = %v, want assistant", second["role"])
	}
	asstBlocks, ok := second["content"].([]any)
	if !ok || len(asstBlocks) != 2 {
		t.Fatalf("assistant content blocks = %v", second["content"])
	}
	textBlock := asstBlocks[0].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] != "Reading." {
		t.Errorf("assistant text block = %v", textBlock)
	}
	toolUse := asstBlocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" {
		t.Errorf("expected tool_use block, got %v", toolUse["type"])
	}
	if toolUse["id"] != "toolu_01" {
		t.Errorf("tool_use.id = %v, want toolu_01", toolUse["id"])
	}
	if toolUse["name"] != "read_file" {
		t.Errorf("tool_use.name = %v, want read_file", toolUse["name"])
	}
	// Critical: input must be a real object, not a stringified JSON
	// blob. Anthropic 400s on the latter.
	input, ok := toolUse["input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_use.input must be an object, got %T (%v)", toolUse["input"], toolUse["input"])
	}
	if input["path"] != "main.go" {
		t.Errorf("tool_use.input.path = %v", input["path"])
	}

	// Message 3: tool_result fed back as a user-role message
	third := msgs[2].(map[string]any)
	if third["role"] != "user" {
		t.Errorf("messages[2].role = %v, want user (tool_result wraps in user)", third["role"])
	}
	resultBlocks, ok := third["content"].([]any)
	if !ok || len(resultBlocks) != 1 {
		t.Fatalf("tool result content = %v", third["content"])
	}
	tr := resultBlocks[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Errorf("expected tool_result block, got %v", tr["type"])
	}
	if tr["tool_use_id"] != "toolu_01" {
		t.Errorf("tool_result.tool_use_id = %v, want toolu_01", tr["tool_use_id"])
	}
}

// TestAnthropicAdapter_AppendsWebSearchToolWhenEnabled covers
// provider-native tool wiring: when EnableWebSearch is set on the
// config, the request body's tools array should contain a
// web_search_20250305 block alongside any user-defined tools. Domain
// filters from cfg.SearchAllowedDomains are forwarded as
// allowed_domains.
//
// Anthropic is opt-in for web_search (unlike OpenAI/xAI which
// default-on) because each call is billed; this test pins both the
// shape and the gate.
func TestAnthropicAdapter_AppendsWebSearchToolWhenEnabled(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_4","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		// httptest uses 127.0.0.1:<random-port>, which detectProvider
		// doesn't recognize as Anthropic — same workaround the existing
		// OpenAI/xAI adapter tests use.
		ProviderOverride:     ProviderAnthropic,
		APIKey:               "k",
		Model:                "claude-sonnet-4-6",
		EnableWebSearch:      true,
		SearchAllowedDomains: []string{"docs.anthropic.com", "example.com"},
	})
	stream := a.ChatStream(context.Background(),
		[]Message{{Role: RoleUser, Content: "search the docs"}},
		nil,
	)
	for range stream {
	}

	body := <-captured
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected tools array in request body, got %v", body["tools"])
	}
	var ws map[string]any
	for _, raw := range tools {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "web_search_20250305" {
			ws = m
			break
		}
	}
	if ws == nil {
		t.Fatalf("tools array missing web_search_20250305 block; got: %v", tools)
	}
	if ws["name"] != "web_search" {
		t.Errorf("web_search.name = %v, want web_search", ws["name"])
	}
	allowed, _ := ws["allowed_domains"].([]any)
	if len(allowed) != 2 {
		t.Fatalf("allowed_domains = %v", ws["allowed_domains"])
	}
	if allowed[0] != "docs.anthropic.com" || allowed[1] != "example.com" {
		t.Errorf("allowed_domains content = %v", allowed)
	}
	if _, has := ws["blocked_domains"]; has {
		t.Errorf("blocked_domains should be omitted when allowed_domains is set")
	}
}

// TestAnthropicAdapter_OmitsWebSearchToolByDefault is the gate-test
// counterpart: without EnableWebSearch, the tools array must NOT
// contain web_search. Anthropic charges per call, so a regression
// where the tool sneaks in by default is a real-money bug.
func TestAnthropicAdapter_OmitsWebSearchToolByDefault(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_5","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL:          srv.URL,
		ProviderOverride: ProviderAnthropic,
		APIKey:           "k",
		Model:            "claude-sonnet-4-6",
	})
	stream := a.ChatStream(context.Background(),
		[]Message{{Role: RoleUser, Content: "hi"}},
		nil,
	)
	for range stream {
	}

	body := <-captured
	if raw, has := body["tools"]; has {
		tools, _ := raw.([]any)
		for _, t2 := range tools {
			m, ok := t2.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "web_search_20250305" {
				t.Fatalf("web_search must not be enabled by default; got tools: %v", tools)
			}
		}
	}
}

// TestUnmarshalToolArgs covers the input-JSON normalization edge
// cases directly. Empty / null / malformed all collapse to {} so the
// SDK's marshaler doesn't choke trying to encode `nil` into the
// tool_use block's required `input` field.
func TestUnmarshalToolArgs(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		isObj   bool
		comment string
	}{
		{`{"path":"x"}`, `map[path:x]`, true, "well-formed object"},
		{`  {"a":1}  `, `map[a:1]`, true, "leading/trailing whitespace"},
		{``, `map[]`, true, "empty -> empty object"},
		{`null`, `map[]`, true, "null -> empty object"},
		{`not json`, `map[]`, true, "garbage -> empty object"},
	}
	for _, c := range cases {
		got := unmarshalToolArgs(c.in)
		if got == nil {
			t.Errorf("[%s] got nil for %q", c.comment, c.in)
			continue
		}
		if c.isObj {
			if _, ok := got.(map[string]any); !ok {
				t.Errorf("[%s] result type = %T, want map[string]any", c.comment, got)
			}
		}
	}
}

// TestAnthropicAdapter_LiftsSystemAndTools verifies that the adapter
// puts the system message in the top-level system param (not as a role
// in messages) and emits the tool catalog as an array of input-schema
// shaped tools. This matters because Anthropic 400s on misplaced
// system content; we want a regression test that fails fast if the
// translator drifts.
func TestAnthropicAdapter_LiftsSystemAndTools(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_2","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "claude-haiku-4-5",
	})

	stream := a.ChatStream(context.Background(),
		[]Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "hi"},
		},
		[]Tool{{Name: "noop", Description: "no", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}}},
	)
	for range stream {
	}

	body := <-captured
	system, ok := body["system"]
	if !ok {
		t.Fatalf("request body missing system; full body: %v", body)
	}
	systemBlocks, ok := system.([]any)
	if !ok || len(systemBlocks) != 1 {
		t.Fatalf("system param shape = %v", system)
	}
	if first, ok := systemBlocks[0].(map[string]any); !ok || first["text"] != "be terse" {
		t.Errorf("system block content = %v", systemBlocks[0])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools array missing or wrong size: %v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "noop" {
		t.Errorf("tool name = %v", tool["name"])
	}
	schema, _ := tool["input_schema"].(map[string]any)
	if schema == nil || schema["type"] != "object" {
		t.Errorf("tool input_schema = %v", tool["input_schema"])
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages array missing or wrong size: %v", body["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("first message should be user, got %v", first["role"])
	}
}

// TestAnthropicAdapter_AnnotatesPromptCacheBreakpoints verifies that
// the Anthropic adapter marks two cache_control breakpoints on every
// outgoing request: the last system block and the last message's last
// content block. With these in place the cache hierarchy
// (tools → system → messages) carries:
//
//   - Breakpoint A: tools array + full system prompt
//   - Breakpoint B: tools + system + entire conversation history
//
// Without this, multi-turn sessions reship the system prompt + tool
// definitions in full on every turn, dominating cost on long agent
// loops. Regression test against a future refactor that drops the
// annotation.
func TestAnthropicAdapter_AnnotatesPromptCacheBreakpoints(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_3","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "claude-sonnet-4-6",
	})

	stream := a.ChatStream(context.Background(),
		[]Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "hello"},
		},
		nil,
	)
	for range stream {
	}

	body := <-captured

	systemBlocks, ok := body["system"].([]any)
	if !ok || len(systemBlocks) == 0 {
		t.Fatalf("system blocks missing or wrong shape: %v", body["system"])
	}
	last := systemBlocks[len(systemBlocks)-1].(map[string]any)
	cc, ok := last["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("last system block missing cache_control; got: %v", last)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type = %v, want ephemeral", cc["type"])
	}

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages missing: %v", body["messages"])
	}
	lastMsg := msgs[len(msgs)-1].(map[string]any)
	contentBlocks, ok := lastMsg["content"].([]any)
	if !ok || len(contentBlocks) == 0 {
		t.Fatalf("last message has no content blocks: %v", lastMsg)
	}
	lastBlock := contentBlocks[len(contentBlocks)-1].(map[string]any)
	mcc, ok := lastBlock["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("last message block missing cache_control; got: %v", lastBlock)
	}
	if mcc["type"] != "ephemeral" {
		t.Errorf("message cache_control type = %v, want ephemeral", mcc["type"])
	}
}

// TestApplyAnthropicCacheControl_EdgeCases pins down the helper's
// no-op behavior on degenerate inputs (no system, no messages, both
// empty). A panic here would bring down every Anthropic turn —
// cheap regression coverage for an otherwise tiny function.
func TestApplyAnthropicCacheControl_EdgeCases(t *testing.T) {
	t.Run("both empty does not panic", func(t *testing.T) {
		applyAnthropicCacheControl(nil, nil)
	})
	t.Run("only system gets marked when messages empty", func(t *testing.T) {
		system, msgs := splitForAnthropic([]Message{
			{Role: RoleSystem, Content: "x"},
		})
		applyAnthropicCacheControl(system, msgs)
		if len(system) != 1 {
			t.Fatalf("system blocks = %d, want 1", len(system))
		}
		if system[0].CacheControl.Type != "ephemeral" {
			t.Errorf("system CacheControl.Type = %q, want ephemeral", system[0].CacheControl.Type)
		}
	})
	t.Run("only message gets marked when system empty", func(t *testing.T) {
		system, msgs := splitForAnthropic([]Message{
			{Role: RoleUser, Content: "x"},
		})
		applyAnthropicCacheControl(system, msgs)
		if len(msgs) != 1 {
			t.Fatalf("messages = %d, want 1", len(msgs))
		}
		blocks := msgs[0].Content
		if len(blocks) == 0 {
			t.Fatalf("message has no content blocks")
		}
		cc := blocks[len(blocks)-1].GetCacheControl()
		if cc == nil || cc.Type != "ephemeral" {
			t.Errorf("last block cache_control = %+v, want ephemeral", cc)
		}
	})
	t.Run("tool result message gets marked", func(t *testing.T) {
		system, msgs := splitForAnthropic([]Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "do x"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", ArgsJSON: `{"path":"a"}`}}},
			{Role: RoleTool, ToolCallID: "t1", Content: "file contents"},
		})
		applyAnthropicCacheControl(system, msgs)
		last := msgs[len(msgs)-1]
		blocks := last.Content
		if len(blocks) == 0 {
			t.Fatalf("last message has no content blocks")
		}
		cc := blocks[len(blocks)-1].GetCacheControl()
		if cc == nil || cc.Type != "ephemeral" {
			t.Errorf("tool-result block cache_control = %+v, want ephemeral", cc)
		}
	})
}

// ---- mock plumbing ----

type anthropicSSE struct {
	Event string
	Data  string
}

type anthropicScript struct {
	events      []anthropicSSE
	captureBody chan<- map[string]any
}

// newAnthropicMockServer spins up an httptest server that responds to
// POST /v1/messages with the given SSE script. Optionally captures the
// decoded request body so tests can assert request shape.
func newAnthropicMockServer(t *testing.T, script anthropicScript) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if script.captureBody != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			select {
			case script.captureBody <- body:
			default:
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ev := range script.events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	return httptest.NewServer(mux)
}
