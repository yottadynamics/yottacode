package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestContextCompactedHandlerRefreshesAndBumpsSeq(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "short"}}
	m.contextTokens = 999
	m.lastWatermarkPct = 0.91
	m.compactionSeq = 3

	out, _ := m.handleAgentEvent(agent.ContextCompacted{Before: 9000, After: 1000, SnapshotPath: "/tmp/pre.json", Forced: true})
	m = out.(Model)

	if m.compactionSeq != 4 {
		t.Fatalf("compactionSeq = %d, want 4", m.compactionSeq)
	}
	if m.lastWatermarkPct != 0.91 {
		t.Fatalf("lastWatermarkPct changed to %.2f", m.lastWatermarkPct)
	}
	if m.contextTokens == 999 {
		t.Fatal("context tokens were not refreshed")
	}
	if got := m.transcript.String(); !strings.Contains(got, "◇ context · compacted") || !strings.Contains(got, "full history saved") {
		t.Fatalf("transcript missing compaction status: %q", got)
	}
}

// TestContextCompactedHandlerRecordsCompaction: the main loop's own
// in-loop mid-turn self-compaction (agent.ContextCompacted, wired whenever
// agentruntime.Build resolves a compaction window — not subagent-only)
// must land in Session.CompactionEvents the same as the turn-boundary
// /summarize path does, so /usage's "compacted Nx" count doesn't silently
// miss this mechanism.
func TestContextCompactedHandlerRecordsCompaction(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "short"}}

	out, _ := m.handleAgentEvent(agent.ContextCompacted{Before: 9000, After: 1000})
	m = out.(Model)

	if got := len(m.sess.CompactionEvents); got != 1 {
		t.Fatalf("CompactionEvents has %d entries, want 1", got)
	}
	rec := m.sess.CompactionEvents[0]
	if rec.Before != 9000 || rec.After != 1000 {
		t.Errorf("CompactionEvents[0] = %+v, want {Before:9000 After:1000}", rec)
	}
}

// TestContextCompactedHandlerSkipsRecordingOnError: a compaction attempt
// that failed (Err set, history left untouched) must not be counted as a
// real compaction — Before==After in that case anyway, so counting it
// would inflate the event count without reclaiming anything.
func TestContextCompactedHandlerSkipsRecordingOnError(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "short"}}

	out, _ := m.handleAgentEvent(agent.ContextCompacted{Before: 9000, After: 9000, Err: errors.New("summary call failed")})
	m = out.(Model)

	if got := len(m.sess.CompactionEvents); got != 0 {
		t.Fatalf("CompactionEvents has %d entries, want 0 on a skipped/failed compaction", got)
	}
}

func TestSummaryDoneDiscardedWhenCompactionSeqMoved(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "current"}}
	m.compactionSeq = 2
	m.lastWatermarkPct = 0.9

	m, _ = applyMsg(m, summaryDoneMsg{
		compactionSeq: 1,
		newMessages:   []adapter.Message{{Role: adapter.RoleUser, Content: "stale"}},
	})

	if got := m.sess.Messages[0].Content; got != "current" {
		t.Fatalf("stale summary overwrote history: %q", got)
	}
	if m.lastWatermarkPct != 0 {
		t.Fatalf("lastWatermarkPct = %.2f, want reset", m.lastWatermarkPct)
	}
}

func TestRefreshTurnCompactionConfigDisablesWhenFloorTooHigh(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow:       1000,
		CompactionThreshold: 0.90,
	}}
	m.cfg.Compaction = &agent.CompactionConfig{Window: 1000, Threshold: 0.90}
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: strings.Repeat("s", 900*4)}}

	m.refreshTurnCompactionConfig()
	if m.cfg.Compaction.Window != 0 {
		t.Fatalf("compaction window = %d, want disabled", m.cfg.Compaction.Window)
	}
}
