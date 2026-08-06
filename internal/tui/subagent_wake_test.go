package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestReconcileSubagentCompletions: a terminal background task whose inbox
// event was dropped (never bannered) is surfaced by the reconciliation, and
// its notify_on_done flag queues a wake. Running and foreground tasks are
// ignored, and a second pass is a no-op (idempotent).
func TestReconcileSubagentCompletions(t *testing.T) {
	reg := subagents.NewRegistry()
	// A dropped, notify_on_done background completion.
	reg.Add(&subagents.Task{ID: "deadbeefcafef00d", AgentType: "review", Status: subagents.TaskRunning, Background: true, NotifyOnDone: true})
	reg.MarkDone("deadbeefcafef00d", subagents.TaskCompleted, "the result", false, 0)
	// Still running — must not be surfaced.
	reg.Add(&subagents.Task{ID: "1111running11111", AgentType: "review", Status: subagents.TaskRunning, Background: true})
	// Foreground terminal — must not be surfaced (only bg completions heal).
	reg.Add(&subagents.Task{ID: "2222foreground22", AgentType: "fg", Status: subagents.TaskRunning, Background: false})
	reg.MarkDone("2222foreground22", subagents.TaskCompleted, "fg result", false, 0)
	// A rehydrated (historical) bg completion from a PRIOR session — even
	// with notify_on_done, it must NOT re-banner or re-wake on resume.
	reg.Import([]subagents.TaskRecord{{ID: "historicalbg0001", AgentType: "review", Status: subagents.TaskCompleted, Result: "old result", Background: true, NotifyOnDone: true}})

	m := Model{subagentTasks: reg, banneredSubagentDone: map[string]bool{}, transcript: &strings.Builder{}}
	if !m.reconcileSubagentCompletions() {
		t.Fatal("reconcile should have surfaced the dropped completion")
	}
	if !m.banneredSubagentDone["deadbeefcafef00d"] {
		t.Error("the bg completion must be marked bannered after reconcile")
	}
	if len(m.pendingSubagentWakes) != 1 || m.pendingSubagentWakes[0].TaskID != "deadbeefcafef00d" {
		t.Errorf("notify_on_done bg completion must be queued as a wake; got %v", m.pendingSubagentWakes)
	}
	if m.banneredSubagentDone["1111running11111"] || m.banneredSubagentDone["2222foreground22"] {
		t.Error("running and foreground tasks must not be reconciled")
	}
	if m.banneredSubagentDone["historicalbg0001"] {
		t.Error("a rehydrated historical task must not be re-bannered on resume")
	}
	if m.reconcileSubagentCompletions() {
		t.Error("second reconcile must be a no-op (already bannered)")
	}
}

// TestBuildSubagentWakeMessage_Single: a single completion injects the
// agent type, the 8-char task id, the status, and the FULL result so the
// model can act without a separate get_subagent_result call.
func TestBuildSubagentWakeMessage_Single(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{
		{TaskID: "abcdef1234567890", AgentType: "review", Result: "found a bug at x.go:10"},
	})
	for _, want := range []string{"review", "abcdef12", "completed", "found a bug at x.go:10"} {
		if !strings.Contains(msg, want) {
			t.Errorf("wake message missing %q; got:\n%s", want, msg)
		}
	}
	// It must read as an async completion, not as the user typing.
	if !strings.Contains(msg, "background subagent you dispatched has finished") {
		t.Errorf("wake message must frame itself as an async completion; got:\n%s", msg)
	}
}

// TestBuildSubagentWakeMessage_MultipleAndErrored: several completions
// collapse into one wake turn, the count is stated, and an errored result
// is labelled errored (so the model decides retry-vs-move-on).
func TestBuildSubagentWakeMessage_MultipleAndErrored(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{
		{TaskID: "aaaa1111bbbb2222", AgentType: "review", Result: "ok"},
		{TaskID: "cccc3333dddd4444", AgentType: "code-verifier", Result: "boom", Errored: true},
	})
	for _, want := range []string{"2 background subagents", "review", "code-verifier", "errored"} {
		if !strings.Contains(msg, want) {
			t.Errorf("multi-wake message missing %q; got:\n%s", want, msg)
		}
	}
}

func TestBuildSubagentWakeMessage_DispatchCommitMetadata(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{
		{
			TaskID:    "dddd1111eeee2222",
			AgentType: "implement",
			Result:    "changed owned files",
			BatchID:   "dispatch-abcd1234",
			Branch:    "worktree-dispatch-abcd1234-1",
			Committed: true,
			CommitSHA: "1234567890abcdef",
		},
	})
	for _, want := range []string{"dispatch-abcd1234", "worktree-dispatch-abcd1234-1", "1234567890abcdef", "call integrate"} {
		if !strings.Contains(msg, want) {
			t.Errorf("dispatch wake message missing %q; got:\n%s", want, msg)
		}
	}
}

func TestBuildSubagentWakeMessage_DispatchCommitFailureMetadata(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{
		{TaskID: "eeee1111ffff2222", AgentType: "test", Result: "hook failed", BatchID: "dispatch-deadbeef", Branch: "worktree-dispatch-deadbeef-2", CommitErr: "pre-commit hook rejected the commit"},
		{TaskID: "ffff1111aaaa2222", AgentType: "docs", Result: "nothing to do", BatchID: "dispatch-deadbeef", Branch: "worktree-dispatch-deadbeef-3", Reclaimed: true},
	})
	for _, want := range []string{"NOT committed", "pre-commit hook rejected", "empty worktree and branch reclaimed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("dispatch wake message missing %q; got:\n%s", want, msg)
		}
	}
}

// TestShortTaskID tolerates ids shorter than 8 chars (test fixtures) and
// truncates longer ones to the 8-char prefix used across the subagent UI.
func TestShortTaskID(t *testing.T) {
	if got := shortTaskID("abcdef1234567890"); got != "abcdef12" {
		t.Errorf("shortTaskID(16 chars) = %q, want abcdef12", got)
	}
	if got := shortTaskID("abc"); got != "abc" {
		t.Errorf("shortTaskID(short) = %q, want abc", got)
	}
}

// TestInboxEventAfterReconcileIsNoOp pins the dedup guard on the
// SubagentBackgroundDone inbox arm. MarkDone lands in the registry
// strictly before the inbox send, so a completion racing a turn
// boundary can be bannered + wake-queued by reconcileSubagentCompletions
// BEFORE the delivered event is processed; without the guard the same
// task double-bannered and queued a second wake turn (duplicate spend,
// possibly duplicate actions).
func TestInboxEventAfterReconcileIsNoOp(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "feedfacefeedface", AgentType: "review", Status: subagents.TaskRunning, Background: true, NotifyOnDone: true})
	reg.MarkDone("feedfacefeedface", subagents.TaskCompleted, "the result", false, 0)

	tr := &strings.Builder{}
	m := Model{
		subagentTasks:        reg,
		banneredSubagentDone: map[string]bool{},
		transcript:           tr,
		subagentInbox:        make(chan agent.SubagentBackgroundDone, 1),
	}
	if !m.reconcileSubagentCompletions() {
		t.Fatal("reconcile should banner + queue the wake first")
	}
	bannerLen := tr.Len()
	if len(m.pendingSubagentWakes) != 1 {
		t.Fatalf("reconcile should queue exactly one wake; got %d", len(m.pendingSubagentWakes))
	}

	// The still-queued inbox event for the same task arrives next.
	m.turnActive = true // hold the wake at the boundary, as in the real race
	next, _ := m.handleAgentEvent(agent.SubagentBackgroundDone{
		TaskID: "feedfacefeedface", AgentType: "review", Result: "the result", NotifyOnDone: true,
	})
	nm := next.(Model)
	if tr.Len() != bannerLen {
		t.Errorf("duplicate inbox event must not re-banner; transcript grew:\n%s", tr.String())
	}
	if len(nm.pendingSubagentWakes) != 1 {
		t.Errorf("duplicate inbox event must not queue a second wake; got %d", len(nm.pendingSubagentWakes))
	}
}

// TestCanceledTaskBannersButNeverWakes covers both wake paths: a task
// the user killed via /subagents stop still banners (the cancellation
// is acknowledged on screen) but must NOT wake the model — the wake
// message nudges "decide whether to retry", and re-running work the
// user deliberately killed is the wrong default.
func TestCanceledTaskBannersButNeverWakes(t *testing.T) {
	// Reconcile path.
	reg := subagents.NewRegistry()
	tk := &subagents.Task{ID: "cancelcancel0001", AgentType: "review", Status: subagents.TaskRunning, Background: true, NotifyOnDone: true}
	reg.Add(tk)
	tk.CanceledByUser = true // what Registry.Cancel sets for /subagents stop
	reg.MarkDone("cancelcancel0001", subagents.TaskCanceled, "", true, 0)

	m := Model{subagentTasks: reg, banneredSubagentDone: map[string]bool{}, transcript: &strings.Builder{}}
	if !m.reconcileSubagentCompletions() {
		t.Fatal("canceled completion should still banner via reconcile")
	}
	if len(m.pendingSubagentWakes) != 0 {
		t.Errorf("user-canceled task must not queue a wake via reconcile; got %v", m.pendingSubagentWakes)
	}

	// Inbox path.
	reg2 := subagents.NewRegistry()
	tk2 := &subagents.Task{ID: "cancelcancel0002", AgentType: "review", Status: subagents.TaskRunning, Background: true, NotifyOnDone: true}
	reg2.Add(tk2)
	tk2.CanceledByUser = true
	reg2.MarkDone("cancelcancel0002", subagents.TaskCanceled, "", true, 0)
	m2 := Model{
		subagentTasks:        reg2,
		banneredSubagentDone: map[string]bool{},
		transcript:           &strings.Builder{},
		subagentInbox:        make(chan agent.SubagentBackgroundDone, 1),
		turnActive:           true,
	}
	next, _ := m2.handleAgentEvent(agent.SubagentBackgroundDone{
		TaskID: "cancelcancel0002", AgentType: "review", Errored: true, NotifyOnDone: true,
	})
	nm := next.(Model)
	if !nm.banneredSubagentDone["cancelcancel0002"] {
		t.Error("canceled completion should still banner via the inbox arm")
	}
	if len(nm.pendingSubagentWakes) != 0 {
		t.Errorf("user-canceled task must not queue a wake via the inbox arm; got %v", nm.pendingSubagentWakes)
	}
}

// TestPartitionReadyWakes_HoldsPartialBatch: a dispatch batch finishes
// worker-by-worker. Waking on each completion would burn one turn per worker
// and hand the model a partial picture it can't integrate from, so wakes
// carrying a BatchID are held until the batch drains. Standalone Agent-tool
// background runs (no BatchID) have no siblings and are always ready.
func TestPartitionReadyWakes_HoldsPartialBatch(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "batchworker00001", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	reg.Add(&subagents.Task{ID: "batchworker00002", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	// Worker 1 is done; worker 2 is still running.
	reg.MarkDone("batchworker00001", subagents.TaskCompleted, "wrote alpha.txt", false, 0)

	m := Model{subagentTasks: reg, pendingSubagentWakes: []agent.SubagentBackgroundDone{
		{TaskID: "batchworker00001", AgentType: "writer", BatchID: "b1"},
		{TaskID: "standalone000001", AgentType: "review"},
	}}

	ready, held := m.partitionReadyWakes()
	if len(held) != 1 || held[0].TaskID != "batchworker00001" {
		t.Errorf("the finished worker of a STILL-RUNNING batch must be held; held=%v", held)
	}
	if len(ready) != 1 || ready[0].TaskID != "standalone000001" {
		t.Errorf("a wake with no BatchID has no siblings and must be ready; ready=%v", ready)
	}
}

// TestPartitionReadyWakes_ReleasesDrainedBatch: once the last worker finishes,
// the whole batch releases together so buildSubagentWakeMessage renders it as a
// single wake turn.
func TestPartitionReadyWakes_ReleasesDrainedBatch(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "batchworker00001", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	reg.Add(&subagents.Task{ID: "batchworker00002", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	reg.MarkDone("batchworker00001", subagents.TaskCompleted, "wrote alpha.txt", false, 0)
	reg.MarkDone("batchworker00002", subagents.TaskCompleted, "wrote beta.txt", false, 0)

	m := Model{subagentTasks: reg, pendingSubagentWakes: []agent.SubagentBackgroundDone{
		{TaskID: "batchworker00001", AgentType: "writer", BatchID: "b1", Branch: "worktree-dispatch-b1-1", Committed: true, CommitSHA: "abc1234"},
		{TaskID: "batchworker00002", AgentType: "writer", BatchID: "b1", Branch: "worktree-dispatch-b1-2", Committed: true, CommitSHA: "def5678"},
	}}

	ready, held := m.partitionReadyWakes()
	if len(held) != 0 {
		t.Errorf("a drained batch must release everything; held=%v", held)
	}
	if len(ready) != 2 {
		t.Fatalf("both workers must release together, got %d", len(ready))
	}
	// One message carrying the whole batch is the point of coalescing.
	msg := buildSubagentWakeMessage(ready)
	for _, want := range []string{"2 background subagents", "worktree-dispatch-b1-1", "worktree-dispatch-b1-2", "abc1234", "def5678"} {
		if !strings.Contains(msg, want) {
			t.Errorf("coalesced wake message missing %q; got:\n%s", want, msg)
		}
	}
}

// TestStartSubagentWakeTurn_HeldBatchStartsNoTurn: the gate lives inside
// startSubagentWakeTurn so every call site (turn-boundary drains, the inbox
// arm) is covered by one check. A batch that hasn't drained must start no turn
// and must keep its wakes queued for the last worker to release.
func TestStartSubagentWakeTurn_HeldBatchStartsNoTurn(t *testing.T) {
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "batchworker00001", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	reg.Add(&subagents.Task{ID: "batchworker00002", Status: subagents.TaskRunning, Background: true, BatchID: "b1"})
	reg.MarkDone("batchworker00001", subagents.TaskCompleted, "wrote alpha.txt", false, 0)

	m := Model{subagentTasks: reg, transcript: &strings.Builder{}, pendingSubagentWakes: []agent.SubagentBackgroundDone{
		{TaskID: "batchworker00001", AgentType: "writer", BatchID: "b1"},
	}}

	next, cmd := m.startSubagentWakeTurn()
	if cmd != nil {
		t.Error("a partially finished batch must not start a wake turn")
	}
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("startSubagentWakeTurn returned %T, want Model", next)
	}
	if len(got.pendingSubagentWakes) != 1 {
		t.Errorf("held wakes must stay queued for the last worker to release, got %d", len(got.pendingSubagentWakes))
	}
}
