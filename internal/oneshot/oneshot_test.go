package oneshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
)

// scriptedStreamer is a duplicate of the one in internal/agent — kept here
// because that one is package-private to agent_test.go. We'll dedupe into a
// shared testutil package once enough call sites exist to justify it.
type scriptedStreamer struct {
	turns [][]adapter.StreamEvent
	mu    sync.Mutex
	next  int
}

func (s *scriptedStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.mu.Lock()
	out := make(chan adapter.StreamEvent, 16)
	if s.next >= len(s.turns) {
		s.mu.Unlock()
		close(out)
		return out
	}
	turn := s.turns[s.next]
	s.next++
	s.mu.Unlock()
	go func() {
		defer close(out)
		for _, ev := range turn {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}

func sseToken(s string) adapter.StreamEvent {
	return adapter.StreamEvent{Kind: adapter.EventTokenDelta, Token: s}
}
func sseReason(s string) adapter.StreamEvent {
	return adapter.StreamEvent{Kind: adapter.EventReasoning, Token: s}
}
func sseDone(content string, tcs ...adapter.ToolCall) adapter.StreamEvent {
	msg := &adapter.Message{Role: adapter.RoleAssistant, Content: content, ToolCalls: tcs}
	return adapter.StreamEvent{Kind: adapter.EventDone, Final: msg}
}

func TestOneshot_ContentGoesToStdout(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("Hel"), sseToken("lo"), sseDone("Hello")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stdout.String(), "Hello") {
		t.Errorf("stdout missing content: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "Hello") {
		t.Errorf("content leaked to stderr: %q", stderr.String())
	}
}

// TurnDone leaves a "✻ Thought for Ns" footnote on stderr, mirroring
// the TUI footnote so non-interactive runs (CI, scripts) get the same
// quiet receipt of how long the model was busy.
func TestOneshot_EmitsThoughtForFootnoteOnStderr(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("ok"), sseDone("ok")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	var stdout, stderr bytes.Buffer
	if err := stream(context.Background(), cfg, &hist, &stdout, &stderr); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stderr.String(), "› Thought for") {
		t.Errorf("stderr should carry the 'Thought for Ns' footnote: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Thought for") {
		t.Errorf("footnote should not leak to stdout (it'd corrupt redirects): %q", stdout.String())
	}
}

func TestOneshot_ReasoningGoesToStderr(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseReason("thinking"), sseToken("answer"), sseDone("answer")},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var stdout, stderr bytes.Buffer
	if err := stream(context.Background(), cfg, &hist, &stdout, &stderr); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(stderr.String(), "thinking") {
		t.Errorf("reasoning should be on stderr: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "thinking") {
		t.Errorf("reasoning leaked to stdout: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "answer") {
		t.Errorf("answer missing from stdout: %q", stdout.String())
	}
}

func TestOneshot_ApprovalNeededWithoutBypassErrors(t *testing.T) {
	// A tool call that requires approval, with BypassPermissions=false:
	// the loop emits ApprovalNeeded; oneshot must surface this as an
	// error (no human in the loop) rather than block.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "danger", ArgsJSON: `{}`})},
	}}
	reg := agent.NewRegistry()
	reg.Register(&fakeApprovalTool{name: "danger"})
	cfg := agent.LoopConfig{
		Adapter: streamer, Registry: reg,
		BypassPermissions: false,
		MaxIterations:     3,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error when ApprovalNeeded fires without --bypass-permissions")
	}
	if !strings.Contains(err.Error(), "approval") {
		t.Errorf("error should mention approval; got %v", err)
	}
}

func TestOneshot_PropagatesAdapterError(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: errors.New("boom")}},
	}}
	cfg := agent.LoopConfig{Adapter: streamer, Registry: agent.NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "x"}}

	var stdout, stderr bytes.Buffer
	err := stream(context.Background(), cfg, &hist, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include underlying message; got %v", err)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr should also surface the error: %q", stderr.String())
	}
}

func TestPreflight_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	err := preflight(context.Background(), adapter.Config{
		BaseURL:          srv.URL,
		APIKey:           "bad",
		Model:            "gpt-5",
		ProviderOverride: adapter.ProviderOpenAI,
	})
	if err == nil {
		t.Fatalf("expected preflight error")
	}
	if !strings.Contains(err.Error(), "authentication failed (HTTP 401)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflight_ModelInvisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	err := preflight(context.Background(), adapter.Config{
		BaseURL:          srv.URL,
		APIKey:           "sk-test",
		Model:            "gpt-5",
		ProviderOverride: adapter.ProviderOpenAI,
	})
	if err == nil {
		t.Fatalf("expected preflight error")
	}
	if !strings.Contains(err.Error(), `model "gpt-5" not listed by /models`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeApprovalTool: a Tool that always requires approval but is never executed
// in these tests (the loop never reaches Execute when ApprovalNeeded is denied).
type fakeApprovalTool struct{ name string }

func (m *fakeApprovalTool) Name() string                 { return m.name }
func (m *fakeApprovalTool) Description() string          { return "fake" }
func (m *fakeApprovalTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (m *fakeApprovalTool) RequiresApproval(string) bool { return true }
func (m *fakeApprovalTool) PreviewCall(string) string    { return m.name + "()" }
func (m *fakeApprovalTool) Execute(_ context.Context, _ string) (string, error) {
	return "should not run", nil
}
