package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// panickyTool panics inside Execute — standing in for any tool with a
// latent bug that trips on some input.
type panickyTool struct{ name string }

func (p *panickyTool) Name() string                 { return p.name }
func (p *panickyTool) Description() string          { return "panics on execute" }
func (p *panickyTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (p *panickyTool) RequiresApproval(string) bool { return false }
func (p *panickyTool) ParallelSafe(string) bool     { return true }
func (p *panickyTool) PreviewCall(string) string    { return p.name + "()" }
func (p *panickyTool) Execute(context.Context, string) (string, error) {
	panic("boom inside tool Execute")
}

// TestExecuteToolCall_RecoversPanic verifies a panicking tool degrades to
// a recoverable error tool_result (nil turn error) instead of an uncaught
// panic that crashes the process. Regression for the release audit's
// "no panic recovery on any agent goroutine" finding.
func TestExecuteToolCall_RecoversPanic(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&panickyTool{name: "boom"})
	cfg := LoopConfig{Registry: reg}
	events := make(chan Event, 16)
	decisions := make(chan Decision, 1)

	out, _, denied, err := executeToolCall(
		context.Background(), cfg,
		adapter.ToolCall{ID: "1", Name: "boom", ArgsJSON: "{}"},
		events, decisions,
	)
	if err != nil {
		t.Fatalf("panic should surface as tool_result content, not a turn error: %v", err)
	}
	if denied {
		t.Errorf("denied = true, want false")
	}
	if !strings.Contains(out, "panicked") {
		t.Errorf("tool result should describe the panic, got %q", out)
	}
}

// TestExecuteToolCallsParallel_RecoversPanic verifies one panicking tool
// in a parallel batch neither crashes the batch nor loses the sibling
// tool's result.
func TestExecuteToolCallsParallel_RecoversPanic(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&panickyTool{name: "boom"})
	reg.Register(&mockTool{name: "ok", parallelSafe: true, output: "fine"})
	cfg := LoopConfig{Registry: reg}
	calls := []adapter.ToolCall{
		{ID: "1", Name: "boom", ArgsJSON: "{}"},
		{ID: "2", Name: "ok", ArgsJSON: "{}"},
	}
	events := make(chan Event, 32)

	results, _ := executeToolCallsParallel(context.Background(), cfg, calls, events, nil)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if !strings.Contains(results[0].content, "panicked") {
		t.Errorf("panicked tool result = %q, want a panic message", results[0].content)
	}
	if results[1].content != "fine" {
		t.Errorf("sibling tool result = %q, want %q", results[1].content, "fine")
	}
}
