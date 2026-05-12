package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// newRegistryWithTask is a test helper that builds a subagents.Registry
// with one task in the given terminal state, ready for the get-result
// tool to fetch.
func newRegistryWithTask(t *testing.T, id string, status subagents.TaskStatus, result string, errored bool) *subagents.Registry {
	t.Helper()
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{
		ID:         id,
		AgentType:  "Explore",
		Prompt:     "test prompt",
		Started:    time.Now().Add(-30 * time.Second),
		Status:     subagents.TaskRunning,
		Background: true,
		ToolCalls:  3,
	})
	if status != subagents.TaskRunning {
		reg.MarkDone(id, status, result, errored, 1200)
		reg.SetToolCalls(id, 3)
	}
	return reg
}

func mustResultJSON(t *testing.T, taskID string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestGetSubagentResultTool_CompletedTaskReturnsFinalReply(t *testing.T) {
	reg := newRegistryWithTask(t, "abc12345def67890", subagents.TaskCompleted, "found 7 files referencing ApprovalNeeded", false)
	tool := &GetSubagentResultTool{Tasks: reg}

	out, err := tool.Execute(context.Background(), mustResultJSON(t, "abc12345"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "found 7 files referencing ApprovalNeeded") {
		t.Errorf("output must contain the child's final reply; got: %q", out)
	}
	if !strings.Contains(out, "Status:    done") {
		t.Errorf("output must summarize the status as done; got: %q", out)
	}
}

func TestGetSubagentResultTool_RunningTaskReturnsStatusHint(t *testing.T) {
	reg := newRegistryWithTask(t, "running12345678a", subagents.TaskRunning, "", false)
	reg.AppendActivity("running12345678a", "list_dir(.)")
	reg.AppendActivity("running12345678a", "grep(\"foo\" in .)")
	tool := &GetSubagentResultTool{Tasks: reg}

	// Pass wait_seconds=1 so the timeout fires quickly. The default
	// is 60s which would block the test for a minute on this
	// never-completing task.
	args, _ := json.Marshal(map[string]any{"task_id": "running1", "wait_seconds": 1})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "still running") {
		t.Errorf("expected a 'still running' status hint; got: %q", out)
	}
	if !strings.Contains(out, "list_dir(.)") || !strings.Contains(out, "grep") {
		t.Errorf("expected the recent activity to be surfaced; got: %q", out)
	}
}

func TestGetSubagentResultTool_UnknownIDReturnsRecoverableError(t *testing.T) {
	reg := subagents.NewRegistry()
	tool := &GetSubagentResultTool{Tasks: reg}

	out, err := tool.Execute(context.Background(), mustResultJSON(t, "nope1234"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no subagent task matches") {
		t.Errorf("expected a recoverable error message; got: %q", out)
	}
}

func TestGetSubagentResultTool_AmbiguousPrefixReturnsRecoverableError(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "abcd1111aaaaaaaa", AgentType: "Explore", Started: time.Now()})
	reg.Add(&subagents.Task{ID: "abcd2222bbbbbbbb", AgentType: "Plan", Started: time.Now()})
	tool := &GetSubagentResultTool{Tasks: reg}

	out, err := tool.Execute(context.Background(), mustResultJSON(t, "abcd"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no subagent task matches") {
		t.Errorf("ambiguous prefix should yield the no-match-or-ambiguous error; got: %q", out)
	}
}

func TestGetSubagentResultTool_ErroredTaskReturnsErrorWithDetail(t *testing.T) {
	reg := newRegistryWithTask(t, "errored1234abcd", subagents.TaskErrored,
		"subagent produced no final reply (adapter mid-stream close)", true)
	tool := &GetSubagentResultTool{Tasks: reg}

	out, err := tool.Execute(context.Background(), mustResultJSON(t, "errored1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "errored") {
		t.Errorf("output should label the result as errored; got: %q", out)
	}
	if !strings.Contains(out, "adapter mid-stream close") {
		t.Errorf("output should surface the underlying failure message; got: %q", out)
	}
}

func TestGetSubagentResultTool_MissingTaskIDArgIsFatal(t *testing.T) {
	tool := &GetSubagentResultTool{Tasks: subagents.NewRegistry()}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Errorf("missing task_id should fail with an error")
	}
}

func TestGetSubagentResultTool_DoesNotRequireApproval(t *testing.T) {
	tool := &GetSubagentResultTool{}
	if tool.RequiresApproval("") {
		t.Errorf("get_subagent_result must not require approval — it's read-only and idempotent")
	}
	if !tool.ParallelSafe("") {
		t.Errorf("get_subagent_result should be ParallelSafe (read-only registry lookups)")
	}
}

// TestGetSubagentResultTool_DefaultWaitsForCompletion verifies that
// calling the tool WITHOUT wait_seconds still waits — the default is
// 60s, not 0. This is the fix that prevents the model from polling
// without waiting, getting "still running," and pivoting to doing
// the subagent's work itself.
func TestGetSubagentResultTool_DefaultWaitsForCompletion(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{
		ID:        "defaultwait12345",
		AgentType: "Explore",
		Prompt:    "test",
		Started:   time.Now(),
		Status:    subagents.TaskRunning,
	})
	tool := &GetSubagentResultTool{Tasks: reg}

	// Complete the task 80ms after the call. The default wait is
	// 60s, so the tool should observe the completion well before
	// the timeout.
	go func() {
		time.Sleep(80 * time.Millisecond)
		reg.MarkDone("defaultwait12345", subagents.TaskCompleted, "default-wait answer", false, 50)
	}()

	// NOTE: no wait_seconds in the args. We're testing the default.
	args, _ := json.Marshal(map[string]string{"task_id": "defaultw"})
	start := time.Now()
	out, err := tool.Execute(context.Background(), string(args))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "default-wait answer") {
		t.Errorf("default wait should have unblocked and returned the final reply; got: %q", out)
	}
	if elapsed > 1*time.Second {
		t.Errorf("default-wait call should have unblocked promptly after MarkDone; took %v", elapsed)
	}
}

// TestGetSubagentResultTool_WaitSecondsUnblocksOnCompletion verifies
// the push-style completion flow: a tool call with wait_seconds set
// blocks on the Registry's WaitFor channel, and a background goroutine
// calling MarkDone unblocks the wait. The tool returns the final
// reply, not the still-running hint, even though the task was running
// when the call started.
func TestGetSubagentResultTool_WaitSecondsUnblocksOnCompletion(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{
		ID:        "waittest12345678",
		AgentType: "Explore",
		Prompt:    "test",
		Started:   time.Now(),
		Status:    subagents.TaskRunning,
	})
	tool := &GetSubagentResultTool{Tasks: reg}

	// In a goroutine, mark the task done 50ms after the test starts.
	// The tool's wait_seconds=2 should unblock well before the timeout.
	go func() {
		time.Sleep(50 * time.Millisecond)
		reg.MarkDone("waittest12345678", subagents.TaskCompleted, "the answer is 42", false, 100)
	}()

	args, _ := json.Marshal(map[string]any{"task_id": "waittest1", "wait_seconds": 2})
	start := time.Now()
	out, err := tool.Execute(context.Background(), string(args))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "the answer is 42") {
		t.Errorf("output must contain the unblocked final reply; got: %q", out)
	}
	if strings.Contains(out, "still running") {
		t.Errorf("output must NOT show the still-running hint after wait unblocks; got: %q", out)
	}
	// The wait should have returned shortly after MarkDone (~50ms),
	// well under the 2s budget. We allow generous slack for slow CI.
	if elapsed > 1*time.Second {
		t.Errorf("wait took %v — should have unblocked promptly after MarkDone fired", elapsed)
	}
}

// TestGetSubagentResultTool_WaitSecondsTimeout verifies that when the
// task doesn't complete within wait_seconds, the tool returns the
// current state (still running) rather than blocking forever.
func TestGetSubagentResultTool_WaitSecondsTimeout(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{
		ID:        "timeout123456789",
		AgentType: "Explore",
		Prompt:    "test",
		Started:   time.Now(),
		Status:    subagents.TaskRunning,
	})
	tool := &GetSubagentResultTool{Tasks: reg}

	// No goroutine marks it done — wait should expire.
	args, _ := json.Marshal(map[string]any{"task_id": "timeout1", "wait_seconds": 1})
	start := time.Now()
	out, err := tool.Execute(context.Background(), string(args))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "still running") {
		t.Errorf("output must show still-running hint after timeout; got: %q", out)
	}
	// Should have waited ~1s. Allow slack on both sides.
	if elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("expected wait of ~1s; got %v", elapsed)
	}
}

// TestGetSubagentResultTool_WaitOnAlreadyCompletedReturnsImmediately
// verifies the fast path: when the task is already terminal at call
// time, WaitFor returns a closed channel immediately and the tool
// renders the final state without artificial delay.
func TestGetSubagentResultTool_WaitOnAlreadyCompletedReturnsImmediately(t *testing.T) {
	reg := newRegistryWithTask(t, "already12345abcd", subagents.TaskCompleted, "instant result", false)
	tool := &GetSubagentResultTool{Tasks: reg}

	args, _ := json.Marshal(map[string]any{"task_id": "already1", "wait_seconds": 30})
	start := time.Now()
	out, err := tool.Execute(context.Background(), string(args))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "instant result") {
		t.Errorf("output must show the final reply; got: %q", out)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("already-terminal task should return immediately even with high wait_seconds; took %v", elapsed)
	}
}

// TestRegistry_WaitForBroadcastsToMultipleWaiters verifies that
// multiple concurrent WaitFor calls on the same task all unblock
// when MarkDone fires (channel close, not send). The
// get_subagent_result tool can be ParallelSafe-batched so multiple
// in-flight calls need to all see the completion.
func TestRegistry_WaitForBroadcastsToMultipleWaiters(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{
		ID:      "broadcast1234567",
		Started: time.Now(),
		Status:  subagents.TaskRunning,
	})

	const N = 5
	results := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			<-reg.WaitFor("broadcast1234567")
			results <- struct{}{}
		}()
	}
	// Give the goroutines a moment to register their waiters.
	time.Sleep(20 * time.Millisecond)
	reg.MarkDone("broadcast1234567", subagents.TaskCompleted, "done", false, 0)

	// All N waiters should fire within a generous window.
	for i := 0; i < N; i++ {
		select {
		case <-results:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("waiter %d did not unblock after MarkDone", i)
		}
	}
}
