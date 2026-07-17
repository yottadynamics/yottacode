package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func vertexAnthropicBaseURL(origin string) string {
	return origin + "/v1/projects/test-proj/locations/global"
}

// newTestRequest builds the request the Anthropic SDK would hand the
// middleware: a POST to /v1/messages carrying the typed params as JSON.
func newTestRequest(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

// vertexAnthropicSSE builds a minimal native Anthropic Messages stream —
// the same wire format api.anthropic.com emits, which is the whole reason
// this kind reuses anthropicAdapter.
func vertexAnthropicSSE(events ...[2]string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString("event: " + ev[0] + "\ndata: " + ev[1] + "\n\n")
	}
	return b.String()
}

func vertexAnthropicTextStream() string {
	return vertexAnthropicSSE(
		[2]string{"message_start", `{"type":"message_start","message":{"id":"msg_vrtx_1","role":"assistant","content":[],"model":"claude-sonnet-4-5-20250929"}}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	)
}

// The core of the kind: Vertex takes the model in the URL and rejects a
// body that carries one, or that carries no anthropic_version.
func TestVertexAnthropic_RewritesRequestIntoPublisherShape(t *testing.T) {
	srv, cap := vertexCapturingMockServer(t, vertexAnthropicTextStream())
	tokens := &stubVertexTokens{token: "ya29.test-token"}

	ad := newVertexAnthropicAdapterFor(Config{
		BaseURL: vertexAnthropicBaseURL(srv.URL),
		Model:   "claude-sonnet-4-5@20250929",
	}, tokens)

	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	path, body, header := cap.snapshot()

	// Asserted against the raw RequestURI: Go escaping "@" to %40 or ":"
	// to %3A here would 404 against real Vertex, and r.URL.Path would
	// have hidden it by decoding.
	want := "/v1/projects/test-proj/locations/global/publishers/anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict"
	if path != want {
		t.Errorf("path  = %q\nwant  = %q", path, want)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}
	if _, ok := got["model"]; ok {
		t.Error("body still carries `model`; Vertex rejects it — it belongs in the URL")
	}
	if got["anthropic_version"] != vertexAnthropicVersion {
		t.Errorf("anthropic_version = %v, want %q", got["anthropic_version"], vertexAnthropicVersion)
	}
	// The rewrite must not disturb the rest of the payload.
	if got["max_tokens"] == nil {
		t.Error("max_tokens missing after rewrite")
	}
	if msgs, ok := got["messages"].([]any); !ok || len(msgs) != 1 {
		t.Errorf("messages = %v, want the single user turn to survive the rewrite", got["messages"])
	}

	if h := header.Get("Authorization"); h != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want the ADC bearer", h)
	}
	// A user with $ANTHROPIC_API_KEY set for the plain anthropic provider
	// must not leak it to Google.
	if h := header.Get("X-Api-Key"); h != "" {
		t.Errorf("X-Api-Key = %q, want it stripped on the Vertex path", h)
	}
}

// Non-streaming requests take :rawPredict. The specifier is read back out
// of the body because that is where the SDK recorded the caller's intent.
func TestVertexAnthropic_PicksSpecifierFromStreamFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		want   string
	}{
		{name: "streaming", stream: true, want: ":streamRawPredict"},
		{name: "non-streaming", stream: false, want: ":rawPredict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"claude-opus-4-8@default","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			if tc.stream {
				body = `{"model":"claude-opus-4-8@default","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			}
			req := newTestRequest(t, "https://aiplatform.googleapis.com/v1/messages", body)

			var gotPath string
			mw := vertexAnthropicMiddleware("/v1/projects/p/locations/global")
			_, err := mw(req, func(r *http.Request) (*http.Response, error) {
				gotPath = r.URL.Path
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			if err != nil {
				t.Fatalf("middleware: %v", err)
			}
			if !strings.HasSuffix(gotPath, tc.want) {
				t.Errorf("path = %q, want suffix %q", gotPath, tc.want)
			}
		})
	}
}

// The SDK retries by re-reading GetBody; a one-shot reader would make
// every retry send an empty body against a signed URL.
func TestVertexAnthropic_RewrittenBodyIsReplayable(t *testing.T) {
	req := newTestRequest(t, "https://aiplatform.googleapis.com/v1/messages",
		`{"model":"claude-opus-4-8@default","stream":true,"max_tokens":16,"messages":[]}`)

	mw := vertexAnthropicMiddleware("/v1/projects/p/locations/global")
	var first, second []byte
	_, err := mw(req, func(r *http.Request) (*http.Response, error) {
		first, _ = io.ReadAll(r.Body)
		rc, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		second, _ = io.ReadAll(rc)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if len(first) == 0 || string(first) != string(second) {
		t.Errorf("GetBody replay = %q, want it to match the first read %q", second, first)
	}
	if int(req.ContentLength) != len(first) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(first))
	}
}

// Vertex serves Claude over the identical Messages wire format, so the
// existing streaming/tool/usage mapping must work untouched through the
// rewrite.
func TestVertexAnthropic_ReusesNativeMessagesMapping(t *testing.T) {
	srv, _ := vertexCapturingMockServer(t, vertexAnthropicSSE(
		[2]string{"message_start", `{"type":"message_start","message":{"id":"m","role":"assistant","content":[],"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":11,"output_tokens":0}}}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reading it."}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	))

	ad := newVertexAnthropicAdapterFor(Config{
		BaseURL: vertexAnthropicBaseURL(srv.URL),
		Model:   "claude-sonnet-4-5@20250929",
	}, &stubVertexTokens{token: "t"})

	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read main.go"}}, nil)
	tokens, _, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if strings.Join(tokens, "") != "Reading it." {
		t.Errorf("tokens = %q, want %q", strings.Join(tokens, ""), "Reading it.")
	}
	if final == nil {
		t.Fatal("no final message")
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0].Name != "read_file" {
		t.Fatalf("ToolCalls = %+v, want one read_file call", final.ToolCalls)
	}
	if final.ToolCalls[0].ArgsJSON != `{"path":"main.go"}` {
		t.Errorf("ArgsJSON = %q, want the accumulated input_json_delta", final.ToolCalls[0].ArgsJSON)
	}
	if final.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", final.StopReason)
	}
	if final.Usage == nil || final.Usage.InputTokens != 11 || final.Usage.OutputTokens != 7 {
		t.Errorf("Usage = %+v, want input=11 output=7", final.Usage)
	}
}

func TestVertexAnthropic_BadBaseURLIsConfigError(t *testing.T) {
	for _, tc := range []struct{ name, baseURL string }{
		{name: "empty", baseURL: ""},
		{name: "no project or location", baseURL: "https://aiplatform.googleapis.com/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ad := newVertexAnthropicAdapterFor(Config{
				BaseURL: tc.baseURL,
				Model:   "claude-sonnet-4-5@20250929",
			}, &stubVertexTokens{token: "t"})
			ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
			_, _, _, errs := drainEvents(ch)
			if len(errs) == 0 {
				t.Fatalf("want a config error for base_url %q, got none", tc.baseURL)
			}
		})
	}
}
