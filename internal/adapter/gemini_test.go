package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// geminiSSEBody wraps a list of JSON event payloads as a Gemini SSE
// body. Gemini emits one JSON object per `data:` frame; no
// `[DONE]` terminator on the wire (the server simply closes the
// stream when finished).
func geminiSSEBody(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

// geminiCapturingMockServer returns a server that serves `body` for
// every request and records the request body + path on `captured`.
type geminiCapture struct {
	mu     sync.Mutex
	body   []byte
	url    string
	header http.Header
}

func geminiCapturingMockServer(t *testing.T, body string) (*httptest.Server, *geminiCapture) {
	t.Helper()
	cap := &geminiCapture{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.body = b
		cap.url = r.URL.String()
		cap.header = r.Header.Clone()
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestGemini_StreamsTextContent(t *testing.T) {
	body := geminiSSEBody(
		`{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"!"}],"role":"model"},"finishReason":"STOP"}]}`,
	)
	srv, _ := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)

	tokens, reasoning, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(tokens, ""); got != "Hello world!" {
		t.Errorf("tokens = %q, want %q", got, "Hello world!")
	}
	if len(reasoning) != 0 {
		t.Errorf("expected no reasoning tokens, got %v", reasoning)
	}
	if final == nil || final.Content != "Hello world!" {
		t.Fatalf("final = %+v, want Content='Hello world!'", final)
	}
	if final.StopReason != "STOP" {
		t.Errorf("final.StopReason = %q, want STOP", final.StopReason)
	}
}

func TestGemini_StreamsThinkingAsReasoning(t *testing.T) {
	body := geminiSSEBody(
		`{"candidates":[{"content":{"parts":[{"text":"Let me think","thought":true}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":" about it","thought":true}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"42"}],"role":"model"},"finishReason":"STOP"}]}`,
	)
	srv, _ := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-pro"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "what's the answer?"}}, nil)

	tokens, reasoning, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := strings.Join(reasoning, ""); got != "Let me think about it" {
		t.Errorf("reasoning = %q, want %q", got, "Let me think about it")
	}
	if got := strings.Join(tokens, ""); got != "42" {
		t.Errorf("tokens = %q, want %q", got, "42")
	}
	// Reasoning tokens must NOT leak into Final.Content — that is
	// reserved for the actual user-visible reply, mirroring the
	// Anthropic adapter's invariant.
	if final == nil || final.Content != "42" {
		t.Errorf("final.Content = %q, want %q", final.Content, "42")
	}
}

func TestGemini_StreamsFunctionCall(t *testing.T) {
	body := geminiSSEBody(
		`{"candidates":[{"content":{"parts":[{"text":"I'll check that file."}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"main.go","limit":50}}}],"role":"model"},"finishReason":"STOP"}]}`,
	)
	srv, _ := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "read main.go"}}, []Tool{
		{Name: "read_file", Description: "read a file", Schema: map[string]any{"type": "object"}},
	})

	_, _, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatal("final = nil")
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %v, want 1 entry", final.ToolCalls)
	}
	tc := final.ToolCalls[0]
	if tc.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want read_file", tc.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.ArgsJSON), &args); err != nil {
		t.Fatalf("ArgsJSON not valid JSON: %v (raw: %s)", err, tc.ArgsJSON)
	}
	if args["path"] != "main.go" {
		t.Errorf("args[path] = %v, want main.go", args["path"])
	}
	// Numeric JSON decodes to float64 by default; either is acceptable
	// as long as the value round-trips faithfully.
	if v, ok := args["limit"].(float64); !ok || v != 50 {
		t.Errorf("args[limit] = %v (%T), want 50", args["limit"], args["limit"])
	}
	if tc.ID == "" {
		t.Error("ToolCall.ID must be non-empty (synthesized when Gemini omits one)")
	}
	if final.Content != "I'll check that file." {
		t.Errorf("final.Content = %q, want %q", final.Content, "I'll check that file.")
	}
}

func TestGemini_LiftsSystemMessageToSystemInstruction(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{
		{Role: RoleSystem, Content: "You are terse."},
		{Role: RoleUser, Content: "hi"},
	}, nil)
	_, _, _, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	var req geminiRequest
	if err := json.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("captured body not JSON: %v\nraw: %s", err, cap.body)
	}
	if req.SystemInstruction == nil {
		t.Fatal("expected systemInstruction, got nil")
	}
	if len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != "You are terse." {
		t.Errorf("systemInstruction = %+v, want one text part 'You are terse.'", req.SystemInstruction)
	}
	for _, c := range req.Contents {
		if c.Role == "system" {
			t.Error("system messages must lift to systemInstruction, not stay in contents")
		}
	}
}

func TestGemini_EffortSetsThinkingConfig(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{
		BaseURL:               srv.URL,
		APIKey:                "test",
		Model:                 "gemini-2.5-flash",
		ReasoningEffort:       "high",
		ModelSupportsThinking: boolPtr(true),
	})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	var req geminiRequest
	if err := json.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("captured body not JSON: %v\nraw: %s", err, cap.body)
	}
	if req.GenerationConfig == nil || req.GenerationConfig.ThinkingConfig == nil {
		t.Fatalf("expected thinkingConfig, got %+v", req.GenerationConfig)
	}
	tc := req.GenerationConfig.ThinkingConfig
	if !tc.IncludeThoughts {
		t.Error("includeThoughts should be true so the thinking trace streams as reasoning")
	}
	if want := geminiThinkingBudget("high", boolPtr(true)); tc.ThinkingBudget != want {
		t.Errorf("thinkingBudget = %d, want %d", tc.ThinkingBudget, want)
	}
}

func TestGemini_NoEffortOmitsThinkingConfig(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	// No effort set → generationConfig must not appear at all, so a
	// default Gemini turn is byte-for-byte what it was before /effort.
	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if bytes.Contains(cap.body, []byte("generationConfig")) {
		t.Errorf("generationConfig should be absent when no effort is set: %s", cap.body)
	}
}

func TestGemini_RoundTripsToolResultAsFunctionResponse(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"got it"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	history := []Message{
		{Role: RoleUser, Content: "read main.go"},
		{Role: RoleAssistant, Content: "ok", ToolCalls: []ToolCall{
			{ID: "call_0", Name: "read_file", ArgsJSON: `{"path":"main.go"}`},
		}},
		{Role: RoleTool, ToolCallID: "call_0", Content: "package main"},
	}
	ch := ad.ChatStream(context.Background(), history, nil)
	_, _, _, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	var req geminiRequest
	if err := json.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("captured body not JSON: %v", err)
	}
	if len(req.Contents) != 3 {
		t.Fatalf("Contents len = %d, want 3 (user, model, user-with-functionResponse)", len(req.Contents))
	}
	last := req.Contents[2]
	if last.Role != "user" {
		t.Errorf("tool result content role = %q, want user", last.Role)
	}
	if len(last.Parts) != 1 || last.Parts[0].FunctionResponse == nil {
		t.Fatalf("tool result parts = %+v, want one functionResponse", last.Parts)
	}
	fr := last.Parts[0].FunctionResponse
	if fr.Name != "read_file" {
		t.Errorf("functionResponse.name = %q, want read_file (looked up by ToolCallID)", fr.Name)
	}
	if fr.Response["result"] != "package main" {
		t.Errorf("functionResponse.response.result = %v, want 'package main'", fr.Response["result"])
	}
}

func TestGemini_TranslatesToolsToFunctionDeclarations(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	tools := []Tool{
		{
			Name:        "read_file",
			Description: "read a file",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
	}
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, tools)
	_, _, _, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	var req geminiRequest
	if err := json.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("captured body not JSON: %v", err)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools shape unexpected: %+v", req.Tools)
	}
	decl := req.Tools[0].FunctionDeclarations[0]
	if decl.Name != "read_file" || decl.Description != "read a file" {
		t.Errorf("decl = %+v, want name=read_file desc='read a file'", decl)
	}
	if decl.Parameters["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", decl.Parameters["type"])
	}
}

func TestGemini_PassesAPIKeyHeader(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "secret-key-123", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil)
	_, _, _, _ = drainEvents(ch)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if got := cap.header.Get("x-goog-api-key"); got != "secret-key-123" {
		t.Errorf("x-goog-api-key = %q, want %q", got, "secret-key-123")
	}
	// The legacy ?key= query string is the alternate form; we
	// deliberately use the header so secrets do not appear in URLs.
	if strings.Contains(cap.url, "key=") {
		t.Errorf("URL should not carry ?key=; got %s", cap.url)
	}
}

func TestGemini_PutsModelAndStreamPathInURL(t *testing.T) {
	body := geminiSSEBody(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	srv, cap := geminiCapturingMockServer(t, body)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-pro"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil)
	_, _, _, _ = drainEvents(ch)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	want := "/v1beta/models/gemini-2.5-pro:streamGenerateContent"
	if !strings.HasPrefix(cap.url, want) {
		t.Errorf("URL prefix = %q, want prefix %q", cap.url, want)
	}
	if !strings.Contains(cap.url, "alt=sse") {
		t.Errorf("URL should request SSE format: %s", cap.url)
	}
}

func TestGemini_HTTPErrorSurfaces(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil)
	_, _, final, errs := drainEvents(ch)
	if len(errs) == 0 {
		t.Fatal("expected error from 429, got none")
	}
	if !strings.Contains(errs[0].Error(), "429") {
		t.Errorf("error should mention status code 429, got %v", errs[0])
	}
	if final != nil {
		t.Errorf("expected no final message on error, got %+v", final)
	}
}

func TestGemini_RoutedByModelPrefix(t *testing.T) {
	// Even with no Gemini-shaped base URL, a gemini-* model tag
	// should pick the Gemini adapter — matches the "corporate proxy
	// fronts the Google API" use case the dispatcher comment calls
	// out. APIKey is set so the empty-key gate doesn't intercept.
	cfg := Config{Model: "gemini-2.5-flash", BaseURL: "https://corp-proxy.example.com", APIKey: "test"}
	client := NewWithConfig(cfg)
	if _, ok := client.(*geminiAdapter); !ok {
		t.Errorf("NewWithConfig returned %T, want *geminiAdapter", client)
	}
}

func TestGemini_RoutedByBaseURL(t *testing.T) {
	cfg := Config{Model: "anything", BaseURL: "https://generativelanguage.googleapis.com", APIKey: "test"}
	client := NewWithConfig(cfg)
	if _, ok := client.(*geminiAdapter); !ok {
		t.Errorf("NewWithConfig returned %T, want *geminiAdapter", client)
	}
}

func TestGemini_ProfileSupportsReasoning(t *testing.T) {
	ad := newGeminiAdapter(Config{Model: "gemini-2.5-flash", BaseURL: "https://generativelanguage.googleapis.com"})
	p := ad.Profile()
	if !p.SupportsReasoning {
		t.Error("Gemini profile should report SupportsReasoning=true")
	}
	if p.SupportsWebSearch || p.SupportsCodeInterpreter || p.SupportsXSearch {
		t.Errorf("Gemini profile should not advertise search/code-interp builtins yet: %+v", p)
	}
}
