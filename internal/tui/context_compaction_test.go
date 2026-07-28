package tui

import (
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
