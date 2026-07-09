package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/catalog"
)

// --- request builder ----------------------------------------------------

func TestBuildRequestForcesContract(t *testing.T) {
	body, err := buildOpenAIAuthRequest("gpt-5.5", "", "",
		[]Message{
			{Role: RoleSystem, Content: "stay concise"},
			{Role: RoleUser, Content: "hi"},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["stream"] != true {
		t.Errorf("stream = %v, want true", got["stream"])
	}
	if got["store"] != false {
		t.Errorf("store = %v, want false", got["store"])
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("model = %v", got["model"])
	}
	if got["instructions"] != "stay concise" {
		t.Errorf("instructions = %v", got["instructions"])
	}
}

func TestBuildRequestReasoningEffort(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	// Effort set on a reasoning model → reasoning.effort in the body.
	body, err := buildOpenAIAuthRequest("gpt-5.5", "high", "", msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	reasoning, ok := got["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning object missing: %v", got["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Errorf("effort = %v, want high", reasoning["effort"])
	}

	// No effort → no reasoning key (stay at the backend default).
	body, _ = buildOpenAIAuthRequest("gpt-5.5", "", "", msgs, nil)
	var unset map[string]any
	_ = json.Unmarshal(body, &unset)
	if _, present := unset["reasoning"]; present {
		t.Errorf("reasoning key should be absent when effort is unset, got %v", unset["reasoning"])
	}
}

func TestBuildRequestSynthesizesInstructions(t *testing.T) {
	body, _ := buildOpenAIAuthRequest("gpt-5.5", "", "",
		[]Message{{Role: RoleUser, Content: "hi"}},
		nil,
	)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["instructions"] != OpenAIAuthDefaultInstructions {
		t.Errorf("expected default instructions, got %v", got["instructions"])
	}
}

func TestBuildRequestJoinsMultipleSystemMessages(t *testing.T) {
	body, _ := buildOpenAIAuthRequest("gpt-5.5", "", "",
		[]Message{
			{Role: RoleSystem, Content: "rule one"},
			{Role: RoleSystem, Content: "rule two"},
			{Role: RoleUser, Content: "hi"},
		},
		nil,
	)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["instructions"] != "rule one\n\nrule two" {
		t.Errorf("instructions = %v", got["instructions"])
	}
}

// Empty tool output must still serialize an `output` field on the
// function_call_output item — the Codex backend rejects requests
// where the field is missing with HTTP 400 "Missing required
// parameter: 'input[N].output'". Regression test for the bug where
// `Output string` + `omitempty` silently dropped the field for
// tools that completed with no stdout/stderr.
func TestBuildRequestKeepsEmptyToolOutput(t *testing.T) {
	body, err := buildOpenAIAuthRequest("gpt-5.5", "", "",
		[]Message{
			{Role: RoleUser, Content: "run the tool"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "write_file", ArgsJSON: "{}"}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: ""},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var outputItem map[string]any
	for _, it := range got.Input {
		if it["type"] == "function_call_output" {
			outputItem = it
			break
		}
	}
	if outputItem == nil {
		t.Fatalf("no function_call_output item in input: %+v", got.Input)
	}
	if _, ok := outputItem["output"]; !ok {
		t.Errorf("function_call_output is missing the output field: %+v", outputItem)
	}
	if outputItem["output"] != "" {
		t.Errorf("output = %v, want empty string", outputItem["output"])
	}
}

func TestBuildRequestMapsTools(t *testing.T) {
	tools := []Tool{{
		Name:        "read_file",
		Description: "read a file",
		Schema:      map[string]any{"type": "object"},
	}}
	body, _ := buildOpenAIAuthRequest("gpt-5.5", "", "",
		[]Message{{Role: RoleUser, Content: "hi"}},
		tools,
	)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	rawTools, ok := got["tools"].([]any)
	if !ok || len(rawTools) != 1 {
		t.Fatalf("tools = %v", got["tools"])
	}
	tool := rawTools[0].(map[string]any)
	if tool["name"] != "read_file" || tool["type"] != "function" {
		t.Errorf("tool = %+v", tool)
	}
}

// A non-empty cache key must ride the wire as prompt_cache_key so the
// Codex backend routes successive turns to the same prompt-cache shard;
// an empty key must omit the field so a session-less caller sends the
// pre-existing body shape unchanged.
func TestBuildRequestPromptCacheKey(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	body, err := buildOpenAIAuthRequest("gpt-5.5", "", "sess-20260709-abc", msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["prompt_cache_key"] != "sess-20260709-abc" {
		t.Errorf("prompt_cache_key = %v, want sess-20260709-abc", got["prompt_cache_key"])
	}

	body, _ = buildOpenAIAuthRequest("gpt-5.5", "", "", msgs, nil)
	var unset map[string]any
	_ = json.Unmarshal(body, &unset)
	if _, present := unset["prompt_cache_key"]; present {
		t.Errorf("prompt_cache_key should be absent when the cache key is empty, got %v", unset["prompt_cache_key"])
	}
}

// --- SSE reader ---------------------------------------------------------

// TestConsumeSSE_TruncatedToolCallErrors guards the truncation fix: a
// function_call announced via output_item.added but whose
// function_call_arguments.done never arrives before the stream ends must
// surface EventErr, not a clean EventDone that silently drops the call.
func TestConsumeSSE_TruncatedToolCallErrors(t *testing.T) {
	a := &openAIAuthAdapter{}
	stream := "event: response.output_item.added\n" +
		`data: {"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"read_file"}}` + "\n\n"
	out := make(chan StreamEvent, 16)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(stream), out)
		close(out)
	}()
	var sawErr, sawDone bool
	for ev := range out {
		switch ev.Kind {
		case EventErr:
			sawErr = true
		case EventDone:
			sawDone = true
		}
	}
	if !sawErr {
		t.Error("truncated tool call should surface EventErr")
	}
	if sawDone {
		t.Error("must not emit a clean EventDone when a tool call is incomplete")
	}
}

// TestConsumeSSE_CompleteToolCallSucceeds is the positive control: a
// fully-delivered tool call still emits EventDone carrying it.
func TestConsumeSSE_CompleteToolCallSucceeds(t *testing.T) {
	a := &openAIAuthAdapter{}
	stream := "event: response.output_item.added\n" +
		`data: {"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"read_file"}}` + "\n\n" +
		"event: response.function_call_arguments.done\n" +
		`data: {"item_id":"item_1","arguments":"{\"path\":\"x\"}"}` + "\n\n"
	out := make(chan StreamEvent, 16)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(stream), out)
		close(out)
	}()
	var final *Message
	var sawErr bool
	for ev := range out {
		switch ev.Kind {
		case EventErr:
			sawErr = true
		case EventDone:
			final = ev.Final
		}
	}
	if sawErr {
		t.Fatal("complete stream should not error")
	}
	if final == nil || len(final.ToolCalls) != 1 || final.ToolCalls[0].Name != "read_file" {
		t.Fatalf("expected one read_file tool call, got %+v", final)
	}
}

// TestSSEErrorDetail covers the envelope shapes a mid-stream `error`
// event can carry. The nested case is the Codex backend's real shape
// (verified by probe 2026-06-10); regression: it used to fall through
// to the bare "stream error" placeholder.
func TestSSEErrorDetail(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "codex nested envelope with code",
			raw:  `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}`,
			want: "context_length_exceeded: Your input exceeds the context window of this model. Please adjust your input and try again.",
		},
		{
			name: "top-level message",
			raw:  `{"type":"error","message":"boom"}`,
			want: "boom",
		},
		{
			name: "detail envelope",
			raw:  `{"detail":"Instructions are required"}`,
			want: "Instructions are required",
		},
		{
			name: "code already inside message is not double-prefixed",
			raw:  `{"error":{"code":"rate_limited","message":"rate_limited by upstream"}}`,
			want: "rate_limited by upstream",
		},
		{
			name: "unknown shape falls back to raw payload",
			raw:  `{"weird":true}`,
			want: `{"weird":true}`,
		},
		{
			name: "empty payload falls back to placeholder",
			raw:  ``,
			want: "stream error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sseErrorDetail([]byte(tc.raw)); got != tc.want {
				t.Errorf("sseErrorDetail(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSSEErrorDetailTruncatesHugeRawFallback(t *testing.T) {
	raw := `{"x":"` + strings.Repeat("a", 4096) + `"}`
	got := sseErrorDetail([]byte(raw))
	if len(got) > 600 {
		t.Errorf("raw fallback not truncated: %d chars", len(got))
	}
}

// TestConsumeSSE_ErrorEventSurfacesNestedDetail replays the exact event
// the Codex backend sends on context overflow: the error detail must
// reach the user, and the stream must not also emit EventDone.
func TestConsumeSSE_ErrorEventSurfacesNestedDetail(t *testing.T) {
	a := &openAIAuthAdapter{}
	stream := "event: error\n" +
		`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"
	out := make(chan StreamEvent, 16)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(stream), out)
		close(out)
	}()
	var errMsg string
	var sawDone bool
	for ev := range out {
		switch ev.Kind {
		case EventErr:
			errMsg = ev.Err.Error()
		case EventDone:
			sawDone = true
		}
	}
	if !strings.Contains(errMsg, "context_length_exceeded") || !strings.Contains(errMsg, "exceeds the context window") {
		t.Errorf("error event detail lost: %q", errMsg)
	}
	if sawDone {
		t.Error("must not emit EventDone after a terminal error event")
	}
}

// TestConsumeSSE_ResponseFailedSurfacesError guards the case where the
// backend signals failure only via response.failed (no preceding error
// event) — previously ignored, committing a truncated turn as success.
func TestConsumeSSE_ResponseFailedSurfacesError(t *testing.T) {
	a := &openAIAuthAdapter{}
	stream := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}` + "\n\n"
	out := make(chan StreamEvent, 16)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(stream), out)
		close(out)
	}()
	var errMsg string
	var sawDone bool
	for ev := range out {
		switch ev.Kind {
		case EventErr:
			errMsg = ev.Err.Error()
		case EventDone:
			sawDone = true
		}
	}
	if !strings.Contains(errMsg, "context_length_exceeded") {
		t.Errorf("response.failed detail lost: %q", errMsg)
	}
	if sawDone {
		t.Error("must not emit EventDone after response.failed")
	}
}

// TestConsumeSSE_ResponseIncompleteKeepsPartialContent: an early stop
// (e.g. max_output_tokens) is not an error — the partial text must land
// in a final message carrying the incomplete stop reason.
func TestConsumeSSE_ResponseIncompleteKeepsPartialContent(t *testing.T) {
	a := &openAIAuthAdapter{}
	stream := "event: response.output_text.delta\n" +
		`data: {"delta":"partial answer"}` + "\n\n" +
		"event: response.incomplete\n" +
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n"
	out := make(chan StreamEvent, 16)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(stream), out)
		close(out)
	}()
	var final *Message
	var sawErr bool
	for ev := range out {
		switch ev.Kind {
		case EventErr:
			sawErr = true
		case EventDone:
			final = ev.Final
		}
	}
	if sawErr {
		t.Fatal("response.incomplete should not surface as an error")
	}
	if final == nil || final.Content != "partial answer" {
		t.Fatalf("partial content lost: %+v", final)
	}
	if final.StopReason != "incomplete" {
		t.Errorf("StopReason = %q, want \"incomplete\"", final.StopReason)
	}
}

func TestSSEReaderSimple(t *testing.T) {
	stream := "event: foo\ndata: {\"a\":1}\n\nevent: bar\ndata: {\"b\":2}\n\n"
	r := newSSEReader(strings.NewReader(stream))
	ev1, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	if ev1.Type != "foo" || string(ev1.Data) != `{"a":1}` {
		t.Errorf("ev1 = %+v", ev1)
	}
	ev2, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	if ev2.Type != "bar" || string(ev2.Data) != `{"b":2}` {
		t.Errorf("ev2 = %+v", ev2)
	}
	if _, err := r.next(); err != io.EOF {
		t.Errorf("expected EOF after two events, got %v", err)
	}
}

func TestSSEReaderIgnoresComments(t *testing.T) {
	stream := ": heartbeat\nevent: foo\ndata: {}\n\n"
	r := newSSEReader(strings.NewReader(stream))
	ev, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "foo" {
		t.Errorf("expected foo, got %+v", ev)
	}
}

func TestSSEReaderMultiLineData(t *testing.T) {
	stream := "event: foo\ndata: line one\ndata: line two\n\n"
	r := newSSEReader(strings.NewReader(stream))
	ev, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Data) != "line one\nline two" {
		t.Errorf("data = %q", string(ev.Data))
	}
}

// --- end-to-end ChatStream ---------------------------------------------

// fakeBackend mounts the Codex endpoint with handler-controlled
// behavior; tests inspect captured requests + drive responses.
type fakeBackend struct {
	srv      *httptest.Server
	calls    atomic.Int32
	handler  http.HandlerFunc
	captured []*http.Request
}

func newFakeBackend(t *testing.T, handler http.HandlerFunc) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{handler: handler}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fb.calls.Add(1)
		// Body is consumed by the handler; capture before delegating.
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		fb.captured = append(fb.captured, r)
		fb.handler(w, r)
	})
	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

func writeAuthStore(t *testing.T, dir string, ts openaiauth.TokenSet) string {
	t.Helper()
	path := filepath.Join(dir, "openai-auth.json")
	if err := openaiauth.Save(path, ts); err != nil {
		t.Fatal(err)
	}
	return path
}

// ssePayload composes a synthetic Responses-API event stream that
// exercises the same shape the live Codex backend emits (verified
// empirically — see project memory).
func ssePayload(text string) string {
	delta := func(s string) string {
		return "event: response.output_text.delta\ndata: " +
			toJSON(map[string]any{"type": "response.output_text.delta", "delta": s}) + "\n\n"
	}
	out := "event: response.created\ndata: {}\n\n"
	out += "event: response.in_progress\ndata: {}\n\n"
	out += "event: response.output_item.added\ndata: " +
		toJSON(map[string]any{"item": map[string]any{"type": "message", "id": "msg_1"}}) + "\n\n"
	for _, ch := range []string{text} {
		out += delta(ch)
	}
	out += "event: response.output_item.done\ndata: " +
		toJSON(map[string]any{"item": map[string]any{"type": "message", "status": "completed"}}) + "\n\n"
	out += "event: response.completed\ndata: {}\n\n"
	return out
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestChatStreamSuccess(t *testing.T) {
	fb := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload("hi there")))
	})

	dir := t.TempDir()
	storePath := writeAuthStore(t, dir, openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	src := openaiauth.NewTokenSource(storePath)

	cfg := Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth}
	a := newOpenAIAuthAdapterFor(cfg, src, fb.srv.URL+"/responses")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || final.Content == "" {
		t.Errorf("EventDone has empty Final: %+v", final)
	}
	if got := strings.Join(tokens, ""); got != "hi there" {
		t.Errorf("combined deltas = %q, want %q", got, "hi there")
	}

	// Verify required headers were sent.
	if len(fb.captured) != 1 {
		t.Fatalf("captured %d requests, want 1", len(fb.captured))
	}
	req := fb.captured[0]
	if got := req.Header.Get("Authorization"); got != "Bearer atk" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q", got)
	}
	if got := req.Header.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q", got)
	}
}

func TestChatStreamRefreshOn401(t *testing.T) {
	// Issuer that mints fresh tokens for the source's refresh path.
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"atk-fresh","refresh_token":"rtk","token_type":"bearer","expires_in":3600}`))
	}))
	t.Cleanup(issuer.Close)

	// Backend that returns 401 to the first request and the SSE
	// stream to the second.
	var attempts atomic.Int32
	backend := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"token expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload("retry ok")))
	})

	dir := t.TempDir()
	storePath := writeAuthStore(t, dir, openaiauth.TokenSet{
		AccessToken:  "atk-stale",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour), // claims valid by clock; server disagrees
	})
	src := openaiauth.NewTokenSource(storePath)
	src.SetIssuer(issuer.URL)
	src.SetHTTPClient(issuer.Client())

	cfg := Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth}
	a := newOpenAIAuthAdapterFor(cfg, src, backend.srv.URL+"/responses")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Errorf("expected EventDone after retry")
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("backend called %d times, want 2 (one 401 + one retry)", got)
	}
	// Second request must use the freshly-refreshed token.
	if len(backend.captured) >= 2 {
		if got := backend.captured[1].Header.Get("Authorization"); got != "Bearer atk-fresh" {
			t.Errorf("retry Authorization = %q, want Bearer atk-fresh", got)
		}
	}
}

func TestChatStreamSurfacesErrorDetail(t *testing.T) {
	backend := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"Stream must be set to true"}`))
	})

	dir := t.TempDir()
	storePath := writeAuthStore(t, dir, openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	src := openaiauth.NewTokenSource(storePath)

	cfg := Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth}
	a := newOpenAIAuthAdapterFor(cfg, src, backend.srv.URL+"/responses")

	_, _, _, errs := drainEvents(a.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) != 1 {
		t.Fatalf("expected single error, got %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "Stream must be set to true") {
		t.Errorf("error did not propagate detail: %v", errs[0])
	}
}

// --- 5xx retry ----------------------------------------------------------

func TestChatStreamRetriesOn503ThenSucceeds(t *testing.T) {
	// First request 503, second request streams normally — auto-retry
	// must mask the transient blip without surfacing an error.
	var attempts atomic.Int32
	backend := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`upstream connect error`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload("recovered")))
	})

	src := openaiauth.NewTokenSource(writeAuthStore(t, t.TempDir(), openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	a := newOpenAIAuthAdapterFor(
		Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth},
		src,
		backend.srv.URL+"/responses",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Errorf("expected silent recovery from 503; got errors: %v", errs)
	}
	if final == nil {
		t.Errorf("expected EventDone after 503-then-200")
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("backend called %d times; want 2 (initial 503 + retry)", got)
	}
}

func TestChatStreamGivesUpAfterRepeated503(t *testing.T) {
	// Always 503 — adapter should attempt initial + len(backoffs)
	// retries, then surface the error with attempt-count context.
	var attempts atomic.Int32
	backend := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"upstream gone"}`))
	})

	src := openaiauth.NewTokenSource(writeAuthStore(t, t.TempDir(), openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	// Patch the backoff schedule to keep the test fast — preserve
	// the count so we exercise the "exhaust attempts" boundary.
	original := openAIAuth5xxBackoffs
	openAIAuth5xxBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond}
	t.Cleanup(func() { openAIAuth5xxBackoffs = original })

	a := newOpenAIAuthAdapterFor(
		Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth},
		src,
		backend.srv.URL+"/responses",
	)

	_, _, _, errs := drainEvents(a.ChatStream(context.Background(),
		[]Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) != 1 {
		t.Fatalf("expected single terminal error, got %v", errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "503") || !strings.Contains(msg, "upstream gone") {
		t.Errorf("error should carry status + detail: %q", msg)
	}
	wantAttempts := int32(len(openAIAuth5xxBackoffs) + 1)
	if got := attempts.Load(); got != wantAttempts {
		t.Errorf("backend called %d times; want %d", got, wantAttempts)
	}
}

// --- 429 rate-limit hint ------------------------------------------------

// run429 wires a fake backend that always returns 429 with a
// caller-supplied header set + body, then drains a single ChatStream
// turn and returns the resulting error message. Pulls the boilerplate
// out of the four tests below so each one is just "given headers/body,
// assert the message contains X".
func run429(t *testing.T, headers map[string]string, body string) string {
	t.Helper()
	backend := newFakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	})

	src := openaiauth.NewTokenSource(writeAuthStore(t, t.TempDir(), openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	a := newOpenAIAuthAdapterFor(
		Config{Model: "gpt-5.5", ProviderOverride: ProviderOpenAIAuth},
		src,
		backend.srv.URL+"/responses",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) != 1 {
		t.Fatalf("expected single error, got %v", errs)
	}
	return errs[0].Error()
}

func TestChatStream429ParsesCodexBodyShape(t *testing.T) {
	// Real Codex 429 body shape (verified by probe 2026-05-06):
	// nested `error.resets_in_seconds` + `error.plan_type`. Days
	// because weekly window — minutes/hours wouldn't surface "5d"
	// as a regression in humanizeDuration.
	msg := run429(t,
		nil,
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_in_seconds":513092}}`,
	)
	if !strings.Contains(msg, "The usage limit has been reached") {
		t.Errorf("error missing detail: %q", msg)
	}
	if !strings.Contains(msg, "retry after 5d") {
		t.Errorf("expected day-scale retry hint: %q", msg)
	}
	if !strings.Contains(msg, "(free plan)") {
		t.Errorf("expected plan_type annotation: %q", msg)
	}
	// The 429 path must produce a two-line message — summary on top,
	// timing on the bottom — so renderers can emit it as two
	// independently-prefixed ✗ lines. Single-line drift would
	// regress the user-visible layout.
	lines := strings.Split(msg, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2-line error, got %d: %q", len(lines), msg)
	}
	if strings.Contains(lines[0], "retry after") {
		t.Errorf("first line should be summary, got hint: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "retry after") {
		t.Errorf("second line should start with retry hint: %q", lines[1])
	}
}

func TestChatStream429ParsesCodexResetAfterHeader(t *testing.T) {
	// Body has only the message — header carries the timing. The
	// adapter should fall through to x-codex-primary-reset-after-seconds.
	msg := run429(t,
		map[string]string{
			"x-codex-primary-reset-after-seconds": "120",
			"x-codex-plan-type":                   "premium",
		},
		`{"error":{"message":"limit reached"}}`,
	)
	if !strings.Contains(msg, "retry after 2m") {
		t.Errorf("expected parsed x-codex-* header hint: %q", msg)
	}
	if !strings.Contains(msg, "(premium plan)") {
		t.Errorf("expected plan_type annotation from header: %q", msg)
	}
}

func TestChatStream429ParsesRetryAfterFallback(t *testing.T) {
	// Generic RFC 7231 Retry-After kept as a fallback for any
	// non-Codex 429 surface that ever lands on this adapter.
	msg := run429(t,
		map[string]string{"Retry-After": "60"},
		`{"detail":"limit reached"}`,
	)
	if !strings.Contains(msg, "retry after 1m") {
		t.Errorf("expected parsed Retry-After hint: %q", msg)
	}
}

func TestChatStream429FallsBackToHeaderDump(t *testing.T) {
	// No body timing, no recognized timing headers — adapter dumps
	// any x-codex-* / x-ratelimit-* headers verbatim so the next
	// real 429 reveals the real shape if it ever drifts.
	msg := run429(t,
		map[string]string{
			"x-codex-primary-used-percent": "100",
			"x-ratelimit-remaining":        "0",
		},
		`{"error":{"message":"limit reached"}}`,
	)
	if !strings.Contains(msg, "X-Codex-Primary-Used-Percent=100") {
		t.Errorf("expected verbatim x-codex-* dump: %q", msg)
	}
	if !strings.Contains(msg, "X-Ratelimit-Remaining=0") {
		t.Errorf("expected verbatim x-ratelimit-* dump: %q", msg)
	}
}

func TestChatStream429NoHintFallsThroughCleanly(t *testing.T) {
	// Bare 429 with nothing useful — error should still surface the
	// detail message and not append an empty " — " separator.
	msg := run429(t, nil, `{"detail":"limit reached"}`)
	if !strings.Contains(msg, "limit reached") {
		t.Errorf("error missing detail: %q", msg)
	}
	if strings.Contains(msg, " — ") {
		t.Errorf("expected no hint suffix when no headers/body fields present: %q", msg)
	}
}

// --- model allow-list ---------------------------------------------------

func TestNewOpenAIAuthAdapterRejectsUnknownModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, _ := openaiauth.DefaultModelsPath()
	if err := openaiauth.SaveModels(path, openaiauth.ModelsFile{
		Models: []string{"gpt-5.5"},
	}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}

	a := newOpenAIAuthAdapter(Config{Model: "gpt-4o", ProviderOverride: ProviderOpenAIAuth})
	_, _, _, errs := drainEvents(a.ChatStream(context.Background(), nil, nil))
	if len(errs) != 1 {
		t.Fatalf("expected single error, got %v", errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "not in your account's discovered set") || !strings.Contains(msg, "gpt-5.5") {
		t.Errorf("error message: %q", msg)
	}
}

// --- allow-list drift across layers -------------------------------------

// The adapter and the catalog both consult the same on-disk file
// (~/.yottacode/auth/openai-auth-models.json) for the openai-auth
// model list. This test seeds the file in a tempdir HOME and asserts
// both the adapter's supportedModels() and catalog.Get("openai-auth")
// return the same set — guaranteeing the picker can never offer a
// model the adapter would reject (the bug class the prior drift test
// existed to catch).
func TestOpenAIAuthSingleSourceForModelList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := openaiauth.DefaultModelsPath()
	if err != nil {
		t.Fatalf("DefaultModelsPath: %v", err)
	}
	want := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}
	if err := openaiauth.SaveModels(path, openaiauth.ModelsFile{
		ScannedAt: time.Now(),
		Models:    want,
	}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}

	adapterAllow := supportedModels()
	if len(adapterAllow) != len(want) {
		t.Errorf("supportedModels() = %v, want %v", adapterAllow, want)
	}
	catalogList := catalog.Get("openai-auth")
	if len(catalogList) != len(want) {
		t.Errorf("catalog.Get(\"openai-auth\") returned %d entries, want %d", len(catalogList), len(want))
	}

	adapterIDs := make(map[string]struct{}, len(adapterAllow))
	for _, id := range adapterAllow {
		adapterIDs[id] = struct{}{}
	}
	for _, m := range catalogList {
		if _, ok := adapterIDs[m.ID]; !ok {
			t.Errorf("catalog lists %q but adapter does not", m.ID)
		}
	}
}

// When no models file exists, both the adapter and the catalog must
// return empty — not a hardcoded fallback. The user-facing surfaces
// branch on this to render the "log in to populate" hint.
func TestOpenAIAuthEmptyWhenNoModelsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := supportedModels(); len(got) != 0 {
		t.Errorf("supportedModels() = %v, want empty", got)
	}
	if got := catalog.Get("openai-auth"); len(got) != 0 {
		t.Errorf("catalog.Get(\"openai-auth\") = %v, want empty", got)
	}
}

// Constructing an adapter when the models file is absent should
// return an errored adapter whose ChatStream emits the
// "no models discovered yet" message.
func TestNewOpenAIAuthAdapterNoModelsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := newOpenAIAuthAdapter(Config{Model: "gpt-5.5"})
	if _, ok := c.(*erroredAdapter); !ok {
		t.Fatalf("expected erroredAdapter, got %T", c)
	}
	ch := c.ChatStream(context.Background(), nil, nil)
	var sawErr bool
	for ev := range ch {
		if ev.Err != nil && strings.Contains(ev.Err.Error(), "no models discovered yet") {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Errorf("expected 'no models discovered yet' error event")
	}
}

// Constructing an adapter for a model NOT in the discovered set
// should error with a "not in your account's discovered set" message.
func TestNewOpenAIAuthAdapterModelNotInDiscoveredSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, _ := openaiauth.DefaultModelsPath()
	if err := openaiauth.SaveModels(path, openaiauth.ModelsFile{
		Models: []string{"gpt-5.5"},
	}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	c := newOpenAIAuthAdapter(Config{Model: "gpt-5.4"})
	if _, ok := c.(*erroredAdapter); !ok {
		t.Fatalf("expected erroredAdapter, got %T", c)
	}
	ch := c.ChatStream(context.Background(), nil, nil)
	var sawErr bool
	for ev := range ch {
		if ev.Err != nil && strings.Contains(ev.Err.Error(), "not in your account's discovered set") {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Errorf("expected 'not in your account's discovered set' error event")
	}
}

// Constructing an adapter for a model that IS in the discovered set
// should not return an errored adapter.
func TestNewOpenAIAuthAdapterModelInDiscoveredSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, _ := openaiauth.DefaultModelsPath()
	if err := openaiauth.SaveModels(path, openaiauth.ModelsFile{
		Models: []string{"gpt-5.5", "gpt-5.4"},
	}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	c := newOpenAIAuthAdapter(Config{Model: "gpt-5.4"})
	if c == nil {
		t.Fatal("expected an adapter")
	}
	if _, ok := c.(*erroredAdapter); ok {
		t.Errorf("got erroredAdapter; expected the real adapter for a discovered model")
	}
}
