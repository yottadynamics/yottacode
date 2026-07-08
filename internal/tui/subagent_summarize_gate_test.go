package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestSubagentWakeWaitsForSummarize verifies notify_on_done completions stay
// queued while summarization is saving/indexing its compacted snapshot. The
// wake turn starts only after summaryDoneMsg has landed, preventing overlapping
// session save/recall index paths.
func TestSubagentWakeWaitsForSummarize(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.summarizing = true
	m.pendingSubagentWakes = []agent.SubagentBackgroundDone{{
		TaskID:    "wakewait0000001",
		AgentType: "review",
		Result:    "finished while summarizing",
	}}

	next, cmd := m.startSubagentWakeTurn()
	nm := next.(Model)
	if cmd != nil || nm.turnActive {
		t.Fatal("wake turn must not start while summarization is in flight")
	}
	if len(nm.pendingSubagentWakes) != 1 {
		t.Fatalf("wake should remain queued during summarize; got %d", len(nm.pendingSubagentWakes))
	}

	nm, cmd = applyMsg(nm, summaryDoneMsg{
		newMessages:  []adapter.Message{{Role: adapter.RoleUser, Content: "compacted"}},
		tokensBefore: 100,
		snapshotPath: "snapshot.json",
	})
	if cmd == nil || !nm.turnActive {
		t.Fatal("summary completion should start the queued wake turn")
	}
	if len(nm.pendingSubagentWakes) != 0 {
		t.Fatalf("wake queue should drain after starting the wake turn; got %d", len(nm.pendingSubagentWakes))
	}
	if !strings.Contains(nm.sess.Messages[len(nm.sess.Messages)-1].Content, "finished while summarizing") {
		t.Fatalf("wake result was not injected after summarize: %#v", nm.sess.Messages[len(nm.sess.Messages)-1])
	}
}

// TestInjectSubagentResultBlocksDuringSummarize covers the manual `/subagents`
// injection path: it must not start a model turn while summaryDoneMsg still
// owns the pending compacted session save/index.
func TestInjectSubagentResultBlocksDuringSummarize(t *testing.T) {
	m := newTestModel(t)
	m.summarizing = true
	task := subagents.Task{ID: "manualinject0001", AgentType: "review", Status: subagents.TaskCompleted, Result: "manual result"}

	next, cmd, status := m.injectSubagentResult(task)
	nm := next.(Model)
	if cmd != nil || nm.turnActive {
		t.Fatal("manual injection must not start a turn during summarization")
	}
	if status == "" || !strings.Contains(status, "summarization") {
		t.Fatalf("expected summarization status, got %q", status)
	}
	if len(nm.pendingSubagentWakes) != 0 {
		t.Fatalf("manual injection should not queue while blocked; got %d", len(nm.pendingSubagentWakes))
	}
}
