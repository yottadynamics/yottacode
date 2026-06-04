package adapter

import (
	"context"
	"strings"
	"testing"
)

// runCopilotSSE feeds a crafted SSE stream straight through consumeSSE and
// drains the result, so the tool-call assembly logic can be tested without
// the full token-exchange + HTTP dance.
func runCopilotSSE(t *testing.T, origNames []string, lines ...string) (*Message, []error) {
	t.Helper()
	a := &copilotAdapter{}
	out := make(chan StreamEvent, 64)
	go func() {
		a.consumeSSE(context.Background(), strings.NewReader(strings.Join(lines, "\n")), origNames, out)
		close(out)
	}()
	_, _, final, errs := drainEvents(out)
	return final, errs
}

// TestCopilot_TruncatedToolCallErrors is a regression for the release
// audit's copilot truncated-tool-call finding: a stream that ends
// mid-tool-call (finish_reason=length) leaves the arguments incomplete;
// committing them would run a tool against garbage. consumeSSE must error.
func TestCopilot_TruncatedToolCallErrors(t *testing.T) {
	final, errs := runCopilotSSE(t, []string{"read_file"},
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\": \"ma"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	)
	if len(errs) == 0 {
		t.Fatalf("expected an error for the truncated tool call, got final=%+v", final)
	}
}

// TestCopilot_InvalidToolArgsErrors covers the secondary guard: even when
// finish_reason isn't a truncation marker, unparseable accumulated args
// must not be committed.
func TestCopilot_InvalidToolArgsErrors(t *testing.T) {
	final, errs := runCopilotSSE(t, []string{"read_file"},
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\": \"ma"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	if len(errs) == 0 {
		t.Fatalf("expected an error for unparseable tool args, got final=%+v", final)
	}
}

// TestCopilot_KeepsNonContiguousToolCalls is a regression for the
// dense-index drop: a stream with tool-call indices 0 and 2 (a gap at 1)
// must keep BOTH — the old 0..len-1 loop dropped the call at index 2.
func TestCopilot_KeepsNonContiguousToolCalls(t *testing.T) {
	final, errs := runCopilotSSE(t, []string{"alpha", "gamma"},
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c2","function":{"name":"gamma","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || len(final.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls (indices 0 and 2 both kept), got %+v", final)
	}
	if final.ToolCalls[0].Name != "alpha" || final.ToolCalls[1].Name != "gamma" {
		t.Errorf("tool calls = %q/%q, want alpha/gamma", final.ToolCalls[0].Name, final.ToolCalls[1].Name)
	}
}

// TestCopilot_CompleteToolCallSucceeds is the positive control: a complete
// tool call with valid args and a normal finish reason must NOT error.
func TestCopilot_CompleteToolCallSucceeds(t *testing.T) {
	final, errs := runCopilotSSE(t, []string{"alpha"},
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"alpha","arguments":"{\"x\": 1}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || len(final.ToolCalls) != 1 || final.ToolCalls[0].Name != "alpha" {
		t.Fatalf("want one complete alpha call, got %+v", final)
	}
}
