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

// TestRefreshTurnCompactionConfigDisablesWhenFloorTooHigh pins the last-resort
// case: fixed overhead (system prompt + tool schemas) alone is large enough
// relative to the window that even the minimum retained-tail ratio
// (minCompactionTargetRatio) leaves no room. Below that, refreshTurnCompactionConfig
// gives up rather than degrade further — there's genuinely nothing compaction
// could do for this turn. This is independent of the derived Threshold value
// (see TestRefreshTurnCompactionConfigShrinksTargetRatioWhenTight for the
// case where shrinking the ratio is enough).
func TestRefreshTurnCompactionConfigDisablesWhenFloorTooHigh(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow: 1000,
		AutoThreshold: 0.85,
	}}
	m.cfg.Compaction = &agent.CompactionConfig{}
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: strings.Repeat("s", 900*4)}}

	m.refreshTurnCompactionConfig()
	if m.cfg.Compaction.Window != 0 {
		t.Fatalf("compaction window = %d, want disabled", m.cfg.Compaction.Window)
	}
}

// TestRefreshTurnCompactionConfigShrinksTargetRatioWhenTight pins the
// graceful-degradation path: when the configured retain ratio doesn't leave
// room once fixed overhead is subtracted, the ratio shrinks in steps rather
// than disabling compaction outright — a smaller compaction pass beats none.
func TestRefreshTurnCompactionConfigShrinksTargetRatioWhenTight(t *testing.T) {
	m := newTestModel(t)
	m.fileCfg = config.Config{Context: config.ContextConfig{
		DefaultWindow:         10_000,
		AutoThreshold:         0.85,
		CompactionTargetRatio: 0.35,
	}}
	m.cfg.Compaction = &agent.CompactionConfig{}
	// System prompt ~6K tokens (24K chars / 4) against a 10K window: the
	// default 0.35 ratio (3.5K) would push 6000+3500=9500 close to the
	// window but still positive at 0.35 — use a bigger system prompt to
	// force the shrink path without tripping the full-disable one.
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: strings.Repeat("s", 7000*4)}}

	m.refreshTurnCompactionConfig()
	if m.cfg.Compaction.Window == 0 {
		t.Fatal("compaction should stay enabled by shrinking the ratio, not disable entirely")
	}
	if m.cfg.Compaction.TargetRatio >= 0.35 {
		t.Fatalf("TargetRatio = %.2f, want shrunk below the configured 0.35", m.cfg.Compaction.TargetRatio)
	}
	if m.cfg.Compaction.TargetRatio < minCompactionTargetRatio {
		t.Fatalf("TargetRatio = %.2f, want >= floor %.2f", m.cfg.Compaction.TargetRatio, minCompactionTargetRatio)
	}
}
