package adapter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// responsesSSE wraps a list of JSON event payloads as an SSE body. The
// Responses API emits one JSON event per `data:` line; the SDK parses by
// the embedded `type` field and discards the [DONE] terminator.
func responsesSSE(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func responsesMockServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestResponses_StreamsReasoningAndContent verifies the Responses adapter
// translates `response.reasoning_summary_text.delta` events into
// EventReasoning and `response.output_text.delta` events into
// EventTokenDelta. This is the headline feature the Responses path was
// built for — Chat Completions hides reasoning for o-series / gpt-5
// models, the Responses endpoint is the only way to surface it.
func TestResponses_StreamsReasoningAndContent(t *testing.T) {
	body := responsesSSE(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":1,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Plan"}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"ning"}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":1,"content_index":0,"delta":" world"}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_1"}}`,
	)
	srv := responsesMockServer(t, body)

	ad := newResponsesAdapter(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "gpt-5-thinking"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	tokens, reasoning, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(reasoning, ""); got != "Planning" {
		t.Errorf("reasoning = %q, want Planning", got)
	}
	if got := strings.Join(tokens, ""); got != "Hello world" {
		t.Errorf("tokens = %q, want 'Hello world'", got)
	}
	if final == nil || final.Content != "Hello world" {
		t.Fatalf("final = %+v, want Content='Hello world'", final)
	}
	if final.Role != RoleAssistant {
		t.Errorf("final.Role = %q, want assistant", final.Role)
	}
	if final.StopReason != "" {
		t.Errorf("final.StopReason = %q, want empty", final.StopReason)
	}
}

// TestResponses_StreamsFunctionCall verifies that an output_item.added
// (function_call) followed by a function_call_arguments.done event
// becomes a single neutral ToolCall in the final Message — the same
// shape the Chat Completions adapter produces, so the agent loop
// handles either provider identically.
func TestResponses_StreamsFunctionCall(t *testing.T) {
	body := responsesSSE(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"delta":"\"x.go\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_1","output_index":0,"arguments":"{\"path\":\"x.go\"}"}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_1"}}`,
	)
	srv := responsesMockServer(t, body)

	ad := newResponsesAdapter(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "o3"})
	tools := []Tool{{
		Name:        "read_file",
		Description: "Read a file",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}}
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read x.go"}}, tools)
	_, _, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1", final.ToolCalls)
	}
	tc := final.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("tool call ID = %q, want call_abc", tc.ID)
	}
	if tc.Name != "read_file" {
		t.Errorf("tool call Name = %q, want read_file", tc.Name)
	}
	if tc.ArgsJSON != `{"path":"x.go"}` {
		t.Errorf("tool call ArgsJSON = %q, want {\"path\":\"x.go\"}", tc.ArgsJSON)
	}
}

// TestResponses_TruncatedFunctionCallErrors guards the truncation fix:
// a function_call announced via output_item.added whose
// function_call_arguments.done never arrives before the stream ends must
// surface an error, not a clean final that silently drops the call.
func TestResponses_TruncatedFunctionCallErrors(t *testing.T) {
	body := responsesSSE(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`,
		// stream ends here: no function_call_arguments.done, no response.completed
	)
	srv := responsesMockServer(t, body)

	ad := newResponsesAdapter(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "o3"})
	tools := []Tool{{Name: "read_file", Description: "Read a file", Schema: map[string]any{"type": "object"}}}
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read x.go"}}, tools)
	_, _, final, errs := drainEvents(ch)

	if len(errs) == 0 {
		t.Error("truncated function call should surface an error, not a clean final")
	}
	if final != nil && len(final.ToolCalls) > 0 {
		t.Errorf("must not emit a final carrying the incomplete tool call: %+v", final)
	}
}

// TestResponses_EmitsStreamProgressForFunctionCallArgs guards the
// "0.0 tok/s never moves" fix: when a Responses turn produces only a
// tool call (no reasoning summary, no body text — common with gpt-5*
// at low effort), the function_call_arguments.delta stream is the
// only signal of activity. The adapter surfaces each delta as an
// EventStreamProgress heartbeat so the TUI's tok/s indicator keeps
// climbing instead of sitting at 0.0 the whole turn.
func TestResponses_EmitsStreamProgressForFunctionCallArgs(t *testing.T) {
	body := responsesSSE(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"delta":"\"x.go\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_1","output_index":0,"arguments":"{\"path\":\"x.go\"}"}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_1"}}`,
	)
	srv := responsesMockServer(t, body)

	ad := newResponsesAdapter(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "gpt-5"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read x.go"}}, []Tool{{
		Name: "read_file", Description: "Read a file",
		Schema: map[string]any{"type": "object"},
	}})

	var progress int
	for ev := range ch {
		if ev.Kind == EventStreamProgress {
			progress++
		}
	}
	if progress != 2 {
		t.Errorf("EventStreamProgress count = %d, want 2 (one per function_call_arguments.delta)", progress)
	}
}

func TestResponses_PropagatesIncompleteMessageStatus(t *testing.T) {
	body := responsesSSE(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}`,
		`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial"}]}}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1"}}`,
	)
	srv := responsesMockServer(t, body)

	ad := newResponsesAdapter(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "gpt-5"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if final.StopReason != "incomplete" {
		t.Fatalf("final.StopReason = %q, want incomplete", final.StopReason)
	}
}

// TestRouter_PicksResponsesForOpenAIReasoningModels guards the routing
// predicate: o-series and gpt-5* on api.openai.com → responsesAdapter,
// everything else (gpt-4o, vLLM, Ollama, xAI, etc.) → chatAdapter.
func TestRouter_PicksResponsesForOpenAIReasoningModels(t *testing.T) {
	cases := []struct {
		cfg           Config
		wantResponses bool
	}{
		{Config{BaseURL: "https://api.openai.com/v1", Model: "o1"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "o1-mini"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "o3"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "o4-mini"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-5"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-5.4"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-5-thinking"}, true},

		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "chatgpt-4o-latest"}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1", DisableWebSearch: true}, false},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1", EnableWebSearch: true}, true},
		{Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1", EnableCodeInterpreter: true}, true},

		// Same model name on a non-OpenAI host should NOT route to
		// Responses — the endpoint shape probably isn't supported.
		{Config{BaseURL: "http://localhost:11434/v1", Model: "gpt-5"}, false},
		{Config{BaseURL: "https://api.x.ai/v1", Model: "grok-4"}, true},
		{Config{BaseURL: "https://api.x.ai/v1", Model: "grok-4", DisableWebSearch: true}, false},
		{Config{BaseURL: "https://api.x.ai/v1", Model: "grok-4", EnableXSearch: true}, true},
		{Config{BaseURL: "http://localhost:8000/v1", Model: "qwen3.5"}, false},
	}

	for _, c := range cases {
		got := usesResponsesAPI(c.cfg)
		if got != c.wantResponses {
			t.Errorf("usesResponsesAPI(%+v) = %v, want %v",
				c.cfg, got, c.wantResponses)
		}
	}
}

func TestResponses_IncludesBuiltinToolsAndReasoningEffort(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, responsesSSE(
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1"}}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := NewWithConfig(Config{
		BaseURL:               srv.URL,
		APIKey:                "sk-test",
		Model:                 "gpt-5",
		ProviderOverride:      ProviderOpenAI,
		ReasoningEffort:       "high",
		EnableWebSearch:       true,
		EnableCodeInterpreter: true,
	})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, _, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for _, want := range []string{
		`"type":"web_search_preview_2025_03_11"`,
		`"type":"code_interpreter"`,
		`"effort":"high"`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s\nbody=%s", want, gotBody)
		}
	}
}

func TestResponses_XAIReasoningEffortForMini(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesSSE(
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1"}}`,
		))
	}))
	t.Cleanup(srv.Close)

	// xAI defaults to the Responses path (web search on); grok-3-mini
	// honors reasoning.effort, so "high" must reach the wire.
	ad := NewWithConfig(Config{
		BaseURL:          srv.URL,
		APIKey:           "xai-test",
		Model:            "grok-3-mini",
		ProviderOverride: ProviderXAI,
		ReasoningEffort:  "high",
	})
	if !ad.Profile().UsesResponsesAPI {
		t.Fatalf("xAI should route to the responses path here")
	}
	if _, _, _, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if !strings.Contains(gotBody, `"effort":"high"`) {
		t.Errorf("grok-3-mini request missing reasoning effort\nbody=%s", gotBody)
	}
}

func TestResponses_XAINoReasoningEffortForGrok4(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesSSE(
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1"}}`,
		))
	}))
	t.Cleanup(srv.Close)

	// grok-4 reasons unconditionally and rejects reasoning_effort.
	ad := NewWithConfig(Config{
		BaseURL:          srv.URL,
		APIKey:           "xai-test",
		Model:            "grok-4",
		ProviderOverride: ProviderXAI,
		ReasoningEffort:  "high",
	})
	if _, _, _, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if strings.Contains(gotBody, `"effort"`) {
		t.Errorf("grok-4 must not send reasoning effort\nbody=%s", gotBody)
	}
}

func TestResponses_XAIRoutesBuiltinWebSearchAndEmitsProviderToolEvent(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, responsesSSE(
			`{"type":"response.web_search_call.in_progress","sequence_number":1,"item_id":"ws_1","output_index":0}`,
			`{"type":"response.web_search_call.searching","sequence_number":2,"item_id":"ws_1","output_index":0}`,
			`{"type":"response.web_search_call.completed","sequence_number":3,"item_id":"ws_1","output_index":0}`,
			`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"done"}`,
			`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_1"}}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := NewWithConfig(Config{
		BaseURL:          srv.URL,
		APIKey:           "xai-test",
		Model:            "grok-4.20-reasoning",
		ProviderOverride: ProviderXAI,
		EnableWebSearch:  true,
	})
	if !ad.Profile().UsesResponsesAPI {
		t.Fatalf("xAI with built-in web search should route to responses")
	}
	// Include a local function with the same name to prove xAI hosted
	// web_search wins and avoids the provider's duplicate-tool-name error.
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, []Tool{{
		Name:        "web_search",
		Description: "local fallback search",
		Schema:      map[string]any{"type": "object"},
	}})
	var providerPhases []string
	for ev := range ch {
		if ev.Kind == EventProviderTool {
			providerPhases = append(providerPhases, ev.ProviderToolName+":"+ev.ProviderToolPhase)
		}
	}
	got := strings.Join(providerPhases, ",")
	for _, want := range []string{"web_search:in_progress", "web_search:searching", "web_search:completed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("provider tool phases = %q, want %q", got, want)
		}
	}
	if !strings.Contains(gotBody, `"type":"web_search"`) {
		t.Fatalf("xAI request body missing web_search tool: %s", gotBody)
	}
	if strings.Contains(gotBody, `"type":"function","description":"local fallback search"`) {
		t.Fatalf("xAI request body should not include duplicate local web_search function: %s", gotBody)
	}
}

func TestResponses_XAIIncludesXSearchConfigAndCapturesCitations(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, responsesSSE(
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"xs_1","type":"x_search_call","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"done"}`,
			`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"xs_1","type":"x_search_call","status":"completed"}}`,
			`{"type":"response.output_item.done","sequence_number":4,"output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[{"type":"url_citation","title":"Example","url":"https://example.com","start_index":0,"end_index":4},{"type":"file_citation","file_id":"file_123","filename":"notes.txt","index":0}]}]}}`,
			`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_1"}}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := NewWithConfig(Config{
		BaseURL:               srv.URL,
		APIKey:                "xai-test",
		Model:                 "grok-4.20-reasoning",
		ProviderOverride:      ProviderXAI,
		EnableXSearch:         true,
		XSearchAllowedHandles: []string{"elonmusk"},
		XSearchFromDate:       "2025-10-01",
		XSearchToDate:         "2025-10-10",
	})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if len(final.Citations) != 2 {
		t.Fatalf("citations = %+v, want 2", final.Citations)
	}
	for _, want := range []string{
		`"type":"x_search"`,
		`"allowed_x_handles":["elonmusk"]`,
		`"from_date":"2025-10-01"`,
		`"to_date":"2025-10-10"`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %s\nbody=%s", want, gotBody)
		}
	}
}

// A marshal failure when building a provider-native tool spec must surface as
// an error rather than panicking on the streaming goroutine (regression for
// the old mustJSON panic, which would have crashed the whole process).
func TestOverrideToolUnion_MarshalErrorIsReturned(t *testing.T) {
	// A channel value is not JSON-marshalable, so json.Marshal returns an
	// error — the path that previously panicked.
	if _, err := overrideToolUnion(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("overrideToolUnion returned nil error for an unmarshalable value")
	}

	// The happy path still produces a usable tool spec.
	if _, err := overrideToolUnion(map[string]any{"type": "x_search"}); err != nil {
		t.Fatalf("overrideToolUnion returned error for a valid spec: %v", err)
	}
}
