package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
)

func writeCopilotStore(t *testing.T, dir string, ts copilotauth.TokenSet) string {
	t.Helper()
	path := filepath.Join(dir, "copilot.json")
	if err := copilotauth.Save(path, ts); err != nil {
		t.Fatal(err)
	}
	return path
}

func copilotSSEPayload(text string) string {
	chunks := ""
	for i, ch := range text {
		chunks += fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
			"id":      "chatcmpl-1",
			"object":  "chat.completion.chunk",
			"created": 1,
			"model":   "gpt-4o",
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"content": string(ch),
				},
				"finish_reason": nil,
			}},
		}))
		_ = i
	}
	chunks += fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
		"id":      "chatcmpl-1",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "gpt-4o",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}))
	chunks += "data: [DONE]\n\n"
	return chunks
}

func TestCopilotBuildRequest(t *testing.T) {
	body, err := buildCopilotRequest("gpt-4o",
		[]Message{
			{Role: RoleSystem, Content: "be helpful"},
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
	if got["model"] != "gpt-4o" {
		t.Errorf("model = %v", got["model"])
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v", got["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be helpful" {
		t.Errorf("system msg = %+v", first)
	}
}

func TestCopilotBuildRequestWithTools(t *testing.T) {
	tools := []Tool{{
		Name:        "read_file",
		Description: "read a file",
		Schema:      map[string]any{"type": "object"},
	}}
	body, _ := buildCopilotRequest("gpt-4o",
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
	if tool["type"] != "function" {
		t.Errorf("tool type = %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Errorf("tool name = %v", fn["name"])
	}
}

func TestCopilotBuildRequestWithToolCalls(t *testing.T) {
	body, err := buildCopilotRequest("gpt-4o",
		[]Message{
			{Role: RoleUser, Content: "run the tool"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "write_file", ArgsJSON: `{"path":"a.txt"}`}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "done"},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
	asstMsg := got.Messages[1]
	tcs, ok := asstMsg["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("assistant tool_calls = %v", asstMsg["tool_calls"])
	}
	toolMsg := got.Messages[2]
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v", toolMsg["tool_call_id"])
	}
}

func TestCopilotChatStreamSuccess(t *testing.T) {
	// Set up a fake token endpoint + backend
	var backendURL string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_copilot_test",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": backendURL},
		})
	}))
	defer tokenSrv.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer tid_copilot_test" {
			t.Errorf("Authorization = %q, want Bearer tid_copilot_test", auth)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(copilotSSEPayload("hello")))
	}))
	defer backend.Close()
	backendURL = backend.URL

	dir := t.TempDir()
	storePath := writeCopilotStore(t, dir, copilotauth.TokenSet{GitHubToken: "gho_test"})
	src := copilotauth.NewTokenSource(storePath)
	src.SetHTTPClient(tokenSrv.Client())
	src.SetTokenEndpoint(tokenSrv.URL)

	cfg := Config{Model: "gpt-4o", ProviderOverride: ProviderCopilot}
	a := newCopilotAdapterFor(cfg, src)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || final.Content == "" {
		t.Errorf("EventDone has empty Final: %+v", final)
	}
	if got := strings.Join(tokens, ""); got != "hello" {
		t.Errorf("combined deltas = %q, want %q", got, "hello")
	}
	if final.StopReason != "stop" {
		t.Errorf("stop reason = %q, want %q", final.StopReason, "stop")
	}
}

func TestCopilotChatStreamRefreshOn401(t *testing.T) {
	var attempts atomic.Int32
	var backendURL string

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_fresh",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": backendURL},
		})
	}))
	defer tokenSrv.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"detail":"token expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(copilotSSEPayload("retried")))
	}))
	defer backend.Close()
	backendURL = backend.URL

	dir := t.TempDir()
	storePath := writeCopilotStore(t, dir, copilotauth.TokenSet{GitHubToken: "gho_test"})
	src := copilotauth.NewTokenSource(storePath)
	src.SetHTTPClient(tokenSrv.Client())
	src.SetTokenEndpoint(tokenSrv.URL)

	cfg := Config{Model: "gpt-4o", ProviderOverride: ProviderCopilot}
	a := newCopilotAdapterFor(cfg, src)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Errorf("expected EventDone after 401 retry")
	}
}

func TestCopilotChatStreamSurfacesErrorDetail(t *testing.T) {
	var backendURL string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_test",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": backendURL},
		})
	}))
	defer tokenSrv.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"Invalid model"}}`))
	}))
	defer backend.Close()
	backendURL = backend.URL

	dir := t.TempDir()
	storePath := writeCopilotStore(t, dir, copilotauth.TokenSet{GitHubToken: "gho_test"})
	src := copilotauth.NewTokenSource(storePath)
	src.SetHTTPClient(tokenSrv.Client())
	src.SetTokenEndpoint(tokenSrv.URL)

	cfg := Config{Model: "bad-model", ProviderOverride: ProviderCopilot}
	a := newCopilotAdapterFor(cfg, src)

	_, _, _, errs := drainEvents(a.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) != 1 {
		t.Fatalf("expected single error, got %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "Invalid model") {
		t.Errorf("error did not propagate detail: %v", errs[0])
	}
}

func TestCopilotChatStreamToolCalls(t *testing.T) {
	var backendURL string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_test",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": backendURL},
		})
	}))
	defer tokenSrv.Close()

	toolCallSSE := fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    "call_1",
					"type":  "function",
					"function": map[string]any{
						"name":      "read_file",
						"arguments": "",
					},
				}},
			},
		}},
	}))
	toolCallArgSSE := fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"function": map[string]any{
						"arguments": `{"path":"a.txt"}`,
					},
				}},
			},
		}},
	}))
	doneSSE := fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "tool_calls",
		}},
	}))

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, toolCallSSE)
		io.WriteString(w, toolCallArgSSE)
		io.WriteString(w, doneSSE)
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer backend.Close()
	backendURL = backend.URL

	dir := t.TempDir()
	storePath := writeCopilotStore(t, dir, copilotauth.TokenSet{GitHubToken: "gho_test"})
	src := copilotauth.NewTokenSource(storePath)
	src.SetHTTPClient(tokenSrv.Client())
	src.SetTokenEndpoint(tokenSrv.URL)

	cfg := Config{Model: "gpt-4o", ProviderOverride: ProviderCopilot}
	a := newCopilotAdapterFor(cfg, src)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, final, errs := drainEvents(a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "use a tool"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatal("no final message")
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(final.ToolCalls))
	}
	tc := final.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("tool call ID = %q", tc.ID)
	}
	if tc.Name != "read_file" {
		t.Errorf("tool call name = %q", tc.Name)
	}
	if tc.ArgsJSON != `{"path":"a.txt"}` {
		t.Errorf("tool call args = %q", tc.ArgsJSON)
	}
}

func TestCopilotChatStreamRequiresDoneSentinel(t *testing.T) {
	var backendURL string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid_test",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": backendURL},
		})
	}))
	defer tokenSrv.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, fmt.Sprintf("data: %s\n\n", toJSON(map[string]any{
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"content": "partial"},
				"finish_reason": "stop",
			}},
		})))
		// No data: [DONE] sentinel: EOF must be treated as truncation.
	}))
	defer backend.Close()
	backendURL = backend.URL

	dir := t.TempDir()
	storePath := writeCopilotStore(t, dir, copilotauth.TokenSet{GitHubToken: "gho_test"})
	src := copilotauth.NewTokenSource(storePath)
	src.SetHTTPClient(tokenSrv.Client())
	src.SetTokenEndpoint(tokenSrv.URL)

	a := newCopilotAdapterFor(Config{Model: "gpt-4o", ProviderOverride: ProviderCopilot}, src)
	_, _, final, errs := drainEvents(a.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) == 0 {
		t.Fatal("missing [DONE] should surface an error")
	}
	if final != nil {
		t.Fatalf("missing [DONE] must not emit final: %+v", final)
	}
}
