package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseBody joins a list of JSON chunks into a valid OpenAI SSE body terminated
// by the [DONE] sentinel.
func sseBody(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// mockServer serves a fixed SSE body for any POST and lets tests observe the
// last request that came through.
func mockServer(t *testing.T, body string) *httptest.Server {
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

func drainEvents(ch <-chan StreamEvent) (tokens, reasoning []string, final *Message, errs []error) {
	for ev := range ch {
		switch ev.Kind {
		case EventTokenDelta:
			tokens = append(tokens, ev.Token)
		case EventReasoning:
			reasoning = append(reasoning, ev.Token)
		case EventProviderTool:
		case EventDone:
			final = ev.Final
		case EventErr:
			errs = append(errs, ev.Err)
		}
	}
	return
}

func TestAdapter_StreamsContent(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	tokens, reasoning, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(tokens, ""); got != "Hello" {
		t.Errorf("tokens = %q, want Hello", got)
	}
	if len(reasoning) != 0 {
		t.Errorf("expected no reasoning events, got %v", reasoning)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if final.Content != "Hello" {
		t.Errorf("final.Content = %q, want Hello", final.Content)
	}
	if final.Role != RoleAssistant {
		t.Errorf("final.Role = %q, want assistant", final.Role)
	}
	if final.StopReason != "stop" {
		t.Errorf("final.StopReason = %q, want stop", final.StopReason)
	}
	if len(final.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", final.ToolCalls)
	}
}

func TestAdapter_StreamsReasoningThenContent(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":"Think"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"","reasoning":"ing"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"PONG"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "ping"}}, nil)
	tokens, reasoning, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(reasoning, ""); got != "Thinking" {
		t.Errorf("reasoning = %q, want Thinking", got)
	}
	if got := strings.Join(tokens, ""); got != "PONG" {
		t.Errorf("tokens = %q, want PONG", got)
	}
	if final == nil || final.Content != "PONG" {
		t.Fatalf("final = %+v, want Content=PONG", final)
	}
}

// xAI's Grok models stream chain-of-thought via a non-standard
// `reasoning_content` field on the delta (not the OpenAI-shim `reasoning`
// field that Ollama uses for Qwen 3 / DeepSeek R1). This test pins the
// extractor against that shape so xAI users get the live reasoning
// surface the same as Ollama users do.
func TestAdapter_StreamsReasoningContentFromXAI(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"plot"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4","choices":[{"index":0,"delta":{"reasoning_content":"ting"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "xai-key", "grok-4")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	tokens, reasoning, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(reasoning, ""); got != "plotting" {
		t.Errorf("reasoning = %q, want plotting", got)
	}
	if got := strings.Join(tokens, ""); got != "OK" {
		t.Errorf("tokens = %q, want OK", got)
	}
	if final == nil || final.Content != "OK" {
		t.Fatalf("final = %+v, want Content=OK", final)
	}
}

func TestAdapter_ToolCall(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/tmp/a.txt\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	tools := []Tool{{
		Name: "read_file",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}}
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read"}}, tools)
	_, _, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if final.StopReason != "tool_calls" {
		t.Errorf("final.StopReason = %q, want tool_calls", final.StopReason)
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1", final.ToolCalls)
	}
	tc := final.ToolCalls[0]
	if tc.ID != "call_x" || tc.Name != "read_file" {
		t.Errorf("tool call metadata = %+v", tc)
	}
	if !strings.Contains(tc.ArgsJSON, `"path"`) || !strings.Contains(tc.ArgsJSON, `/tmp/a.txt`) {
		t.Errorf("tool call args = %q", tc.ArgsJSON)
	}
}

func TestAdapter_SplitToolCallArgumentsSucceeds(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x.go\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || len(final.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1", final)
	}
	if got := final.ToolCalls[0].ArgsJSON; got != `{"path":"x.go"}` {
		t.Errorf("ArgsJSON = %q, want path object", got)
	}
}

func TestAdapter_RejectsMalformedToolCalls(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "truncated arguments",
			body: sseBody(
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"write_file","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			),
			want: "invalid tool call write_file arguments",
		},
		{
			name: "missing function name",
			body: sseBody(
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"arguments":"{}"}}]},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			),
			want: "function name is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mockServer(t, tc.body)
			ad := New(srv.URL, "", "test")
			_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read"}}, nil))
			if final != nil {
				t.Fatalf("final should be nil on malformed tool call, got %+v", final)
			}
			if len(errs) == 0 {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("error = %q, want contains %q", errs[0], tc.want)
			}
		})
	}
}

// TestAdapter_NormalizesEmptyToolCallArguments locks the no-argument-tool
// regression: a tool like exit_plan_mode / git_push / worktree_status can
// arrive with an empty arguments payload, which must normalize to "{}" and
// succeed — not hard-error the turn — so the call still runs and serializes as
// valid JSON on replay.
func TestAdapter_NormalizesEmptyToolCallArguments(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"exit_plan_mode","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "go"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || len(final.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1", final)
	}
	if got := final.ToolCalls[0].ArgsJSON; got != "{}" {
		t.Errorf("ArgsJSON = %q, want {}", got)
	}
}

// TestAdapter_RejectsLengthTruncatedToolCall guards the finish_reason
// truncation tell: a tool call cut off by "length" is rejected even when the
// partial arguments happen to parse, so a truncated call never reaches a tool
// or poisons replayed history.
func TestAdapter_RejectsLengthTruncatedToolCall(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"x.go\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "write"}}, nil))
	if final != nil {
		t.Fatalf("final should be nil on truncated tool call, got %+v", final)
	}
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "response truncated") {
		t.Fatalf("errs = %v, want 'response truncated'", errs)
	}
}

// TestToOpenAIMessages_SanitizesReplayedToolCallArgs locks the replay defense:
// history persisted before validation existed — or produced by another adapter
// — can carry an assistant tool_call with empty or truncated arguments.
// Re-serializing it verbatim makes the whole request invalid JSON for strict
// providers (NVIDIA NIM then 400s every later turn). The builder substitutes
// "{}" so the request always parses; well-formed args pass through untouched.
func TestToOpenAIMessages_SanitizesReplayedToolCallArgs(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "write_file", ArgsJSON: ""},             // empty
			{ID: "b", Name: "read_file", ArgsJSON: `{"path":`},      // truncated
			{ID: "c", Name: "glob", ArgsJSON: `{"pattern":"*.go"}`}, // valid
		}},
	}
	out := toOpenAIMessages(msgs)
	var got []string
	for i := range out {
		if a := out[i].OfAssistant; a != nil {
			for _, tc := range a.ToolCalls {
				got = append(got, tc.Function.Arguments)
			}
		}
	}
	want := []string{"{}", "{}", `{"pattern":"*.go"}`}
	if len(got) != len(want) {
		t.Fatalf("replayed args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tool call %d args = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAdapter_EmitsStreamProgressForToolCallDeltas guards the "0.0
// tok/s never moves" fix on the Chat Completions side: when a turn
// produces only a tool call (no content, no reasoning), the
// delta.tool_calls stream is the only signal of activity. The
// adapter surfaces each chunk that carries a tool-call delta as an
// EventStreamProgress heartbeat so the TUI's tok/s indicator keeps
// climbing instead of sitting at 0.0 the whole turn.
func TestAdapter_EmitsStreamProgressForToolCallDeltas(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_y","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x.go\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	tools := []Tool{{Name: "read_file", Schema: map[string]any{"type": "object"}}}
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read"}}, tools)

	var progress int
	for ev := range ch {
		if ev.Kind == EventStreamProgress {
			progress++
		}
	}
	// Three chunks carry a tool-call delta (the open + two
	// argument deltas); the terminal chunk has no tool_calls so it
	// doesn't count.
	if progress != 3 {
		t.Errorf("EventStreamProgress count = %d, want 3", progress)
	}
}

// TestAdapter_HandlesEmptyToolCallsArrayDelta pins the fix for an
// openai-go v1.12.0 panic at streamaccumulator.go:172 when a delta
// chunk carries `"tool_calls": []` — the field is JSON-present but
// the array is empty, and the SDK indexes [0] without checking. NVIDIA
// NIM's reasoning models (e.g. nemotron-super-49b-v1 with "detailed
// thinking on") emit chunks of this shape, which would crash the
// adapter goroutine before this fix.
func TestAdapter_HandlesEmptyToolCallsArrayDelta(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"hi","tool_calls":[]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	tokens, _, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(tokens, ""); got != "hi" {
		t.Errorf("tokens = %q, want hi", got)
	}
	if final == nil || final.Content != "hi" {
		t.Fatalf("final = %+v, want Content=hi", final)
	}
	if len(final.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", final.ToolCalls)
	}
}

func TestAdapter_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, _, errs := drainEvents(ch)

	if len(errs) == 0 {
		t.Fatalf("expected an error, got none")
	}
}

// captureChatRequest runs one ChatStream turn against a server that
// records the outbound request body, then returns it parsed. Used to
// assert what reasoning params the chat adapter put on the wire.
func captureChatRequest(t *testing.T, cfg Config) map[string]any {
	t.Helper()
	captured := make(chan []byte, 1)
	resp := sseBody(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured <- b
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	if cfg.APIKey == "" {
		cfg.APIKey = "test"
	}
	ad := NewWithConfig(cfg)
	if _, _, _, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	var got map[string]any
	if err := json.Unmarshal(<-captured, &got); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	return got
}

func TestChatAdapter_XAIReasoningEffort(t *testing.T) {
	// grok-3-mini accepts reasoning_effort; "high" maps straight through.
	// DisableWebSearch forces the chat-completions path (xAI otherwise
	// routes through the Responses adapter — see responses_test.go).
	got := captureChatRequest(t, Config{
		Model:            "grok-3-mini",
		ProviderOverride: ProviderXAI,
		ReasoningEffort:  "high",
		DisableWebSearch: true,
	})
	if got["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
}

func TestChatAdapter_XAIReasoningEffortOnlyForMini(t *testing.T) {
	// grok-4 reasons unconditionally and rejects reasoning_effort, so
	// the adapter must leave the field off.
	got := captureChatRequest(t, Config{
		Model:            "grok-4",
		ProviderOverride: ProviderXAI,
		ReasoningEffort:  "high",
		DisableWebSearch: true,
	})
	if _, present := got["reasoning_effort"]; present {
		t.Errorf("grok-4 must not receive reasoning_effort, got %v", got["reasoning_effort"])
	}
}

func TestChatAdapter_ReasoningEffortXAIOnly(t *testing.T) {
	// A non-xAI OpenAI-compatible endpoint must never get reasoning_effort,
	// even for a model whose name happens to match — the field is gated
	// on the resolved provider.
	got := captureChatRequest(t, Config{
		Model:            "grok-3-mini",
		ProviderOverride: ProviderOpenAICompatible,
		ReasoningEffort:  "high",
	})
	if _, present := got["reasoning_effort"]; present {
		t.Errorf("non-xAI provider must not receive reasoning_effort, got %v", got["reasoning_effort"])
	}
}

func TestAdapter_PropagatesLengthFinishReason(t *testing.T) {
	body := sseBody(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
	)
	srv := mockServer(t, body)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, final, errs := drainEvents(ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatalf("no final message")
	}
	if final.StopReason != "length" {
		t.Fatalf("final.StopReason = %q, want length", final.StopReason)
	}
}

func TestAdapter_ContextCancel(t *testing.T) {
	// Server hangs forever. We cancel ctx immediately so the client aborts
	// before receiving any chunks.
	blockCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh // held until the test closes it
	}))
	t.Cleanup(func() {
		close(blockCh)
		srv.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil)

	// Drain with a wall-clock guard — if ctx cancellation isn't honored the
	// stream would block forever.
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	var gotErr bool
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if !gotErr {
					t.Fatalf("stream closed without an EventErr")
				}
				return
			}
			if ev.Kind == EventErr {
				gotErr = true
			}
		case <-timeout.C:
			t.Fatalf("cancel was not honored within 2s")
		}
	}
}

func TestAdapter_SendsAuthorizationBearerHeader(t *testing.T) {
	// Verifies the apiKey passed to New() actually reaches the wire as
	// "Authorization: Bearer <key>". This is what NVIDIA NIM, OpenAI,
	// and xAI use to authenticate. Regression test for a 401 we hit
	// when wiring through YOTTACODE_API_KEY.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"t","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := New(srv.URL, "nvapi-test-key", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "ping"}}, nil)
	_, _, _, _ = drainEvents(ch)

	want := "Bearer nvapi-test-key"
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestAdapter_SendsPlaceholderAuthWhenNoAPIKey(t *testing.T) {
	// When no API key is set (e.g., Ollama), we still send a non-empty
	// bearer because openai-go refuses an empty value. The placeholder
	// is harmless — Ollama / Llama Stack ignore the header.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"t","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "ping"}}, nil)
	_, _, _, _ = drainEvents(ch)

	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("Authorization should still send a Bearer placeholder; got %q", gotAuth)
	}
}

// TestAdapter_SetsDefaultMaxTokens guards Fix B: the chatAdapter must
// always send max_tokens. NVIDIA NIM treats a missing value as 0 and
// rejects with 400 once input is anywhere near the model's window,
// even though OpenAI itself tolerates the omission. Reproduces by
// inspecting the JSON body that hits the wire.
func TestAdapter_SetsDefaultMaxTokens(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"t","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	t.Cleanup(srv.Close)

	ad := New(srv.URL, "", "test")
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "ping"}}, nil)
	_, _, _, _ = drainEvents(ch)

	raw, ok := gotBody["max_tokens"]
	if !ok {
		t.Fatalf("request body should include max_tokens; got %+v", gotBody)
	}
	val, ok := raw.(float64) // JSON numbers unmarshal as float64
	if !ok {
		t.Fatalf("max_tokens should be numeric; got %T (%v)", raw, raw)
	}
	if int64(val) != ChatDefaultMaxTokens {
		t.Errorf("max_tokens = %d, want %d", int64(val), ChatDefaultMaxTokens)
	}
}

func TestAdapter_ProfileDetection(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want Provider
	}{
		{"openai", Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1"}, ProviderOpenAI},
		{"xai", Config{BaseURL: "https://api.x.ai/v1", Model: "grok-4"}, ProviderXAI},
		{"ollama", Config{BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:latest"}, ProviderOllama},
		{"generic", Config{BaseURL: "https://example.com/v1", Model: "foo"}, ProviderOpenAICompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ad := NewWithConfig(tc.cfg)
			if got := ad.Profile().Provider; got != tc.want {
				t.Fatalf("Profile().Provider = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToOpenAIMessages_RoundtripAssistantToolCall(t *testing.T) {
	// Assistant messages with tool_calls must serialize to the OpenAI shape
	// without the SDK dropping the tool_calls (which would break multi-turn
	// tool chains on replay).
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID:       "call_1",
			Name:     "read_file",
			ArgsJSON: `{"path":"/etc/hostname"}`,
		}}},
		{Role: RoleTool, Content: "linuxbox", ToolCallID: "call_1"},
	}
	converted := toOpenAIMessages(msgs)
	if len(converted) != len(msgs) {
		t.Fatalf("converted len = %d, want %d", len(converted), len(msgs))
	}
	asst := converted[2].OfAssistant
	if asst == nil {
		t.Fatalf("converted[2].OfAssistant is nil")
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1", asst.ToolCalls)
	}
	if asst.ToolCalls[0].ID != "call_1" || asst.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool call = %+v", asst.ToolCalls[0])
	}
	tool := converted[3].OfTool
	if tool == nil {
		t.Fatalf("converted[3].OfTool is nil")
	}
}
