package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestDrainChildEvents_UnblocksSenderUntilClosed is the deterministic,
// non-flaky proof for drainChildEvents: once installed, a sender that
// would otherwise block on a full buffered channel (nobody left
// reading it) must be able to keep sending — this is exactly what lets
// the child's Turn goroutine finish and return after runChild's
// cancelExit or panic-recovery path stops draining childEvents itself.
func TestDrainChildEvents_UnblocksSenderUntilClosed(t *testing.T) {
	ch := make(chan Event, 2)
	// Fill the buffer synchronously first, proving it starts genuinely
	// full — a further send would block forever without a reader.
	ch <- ToolStart{ToolName: "already-buffered-1"}
	ch <- ToolStart{ToolName: "already-buffered-2"}

	drainChildEvents(ch)

	done := make(chan struct{})
	go func() {
		for range 20 {
			ch <- ToolStart{ToolName: "more"}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sends on ch blocked — drainChildEvents did not keep consuming")
	}
	close(ch)
}

// TestAgentTool_RunChildPanicInOrchestration_ClosesTranscript
// reproduces the transcript-leak-on-panic bug: runChild's top-level
// panic-recovery defer degraded the task to errored but never closed
// the transcript's *os.File, unlike every other exit path. Triggered
// here via a poisoned emitToParent — runChild calls it from its drain
// loop (emitActivity) for ordinary progress events like ToolStart, so
// a panic there simulates "a panic anywhere in the child's
// orchestration," the exact class the recover is documented to guard.
func TestAgentTool_RunChildPanicInOrchestration_ClosesTranscript(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "read_file", ArgsJSON: `{}`})},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "read_file", output: "file body"})

	tool := &AgentTool{
		Tasks:    subagents.NewRegistry(),
		Cwd:      NewCwdRef(t.TempDir()),
		YoloMode: &YoloModeState{},
		PlanMode: &PlanModeState{},
		AutoMode: &AutoModeState{},
	}
	cfg := &subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	taskID := subagents.NewTaskID()
	transcriptDir := t.TempDir()
	transcriptPath := filepath.Join(transcriptDir, "fg-"+taskID+".md")
	transcript := openTranscript(transcriptPath, cfg, agentArgs{Prompt: "p"})
	tool.Tasks.Add(&subagents.Task{ID: taskID, AgentType: cfg.Name, Status: subagents.TaskRunning, TranscriptPath: transcriptPath})

	// Capture the real *os.File before runChild can nil it out via
	// close(), so we can independently verify the fd was actually
	// released (not just the field zeroed).
	underlyingFile := transcript.f
	if underlyingFile == nil {
		t.Fatal("openTranscript did not open a real file — test setup broken")
	}

	panicEmit := func(Event) { panic("boom in orchestration") }

	result, errored, status, _ := tool.runChild(
		context.Background(), taskID, cfg, "p", transcript, panicEmit, nil,
		streamer, "", childRunOpts{reg: reg},
	)

	if !errored || status != subagents.TaskErrored {
		t.Fatalf("expected errored=true status=TaskErrored after a recovered panic, got errored=%v status=%v", errored, status)
	}
	if !strings.Contains(result, "boom in orchestration") {
		t.Errorf("result should surface the recovered panic; got %q", result)
	}

	// The fix: close() runs via the panic-recovery defer and nils tr.f.
	if transcript.f != nil {
		t.Error("transcript.f is still set after a panic in runChild — the file was not closed")
	}
	// Independently confirm the underlying fd was actually released:
	// closing an already-closed *os.File returns an error.
	if err := underlyingFile.Close(); err == nil {
		t.Error("transcript's underlying file was still open after the panic — expected it to already be closed")
	} else if !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected an already-closed error on the second Close, got %v", err)
	}
}
