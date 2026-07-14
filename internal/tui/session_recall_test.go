package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/recall"
)

func TestRenderTurnStatus_RecalledIndicator(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now()

	// No recall this turn → no "recalled" segment.
	if s := m.renderTurnStatus(); strings.Contains(s, "recalled") {
		t.Errorf("no-recall status should omit indicator: %q", s)
	}

	// Plural.
	m.recalledCount.Store(2)
	if s := m.renderTurnStatus(); !strings.Contains(s, "recalled 2 conversations") {
		t.Errorf("status = %q, want 'recalled 2 conversations'", s)
	}

	// Singular.
	m.recalledCount.Store(1)
	s := m.renderTurnStatus()
	if !strings.Contains(s, "recalled 1 conversation") || strings.Contains(s, "1 conversations") {
		t.Errorf("singular status = %q, want 'recalled 1 conversation'", s)
	}
}

func hit(id, name, snippet string, score float64) recall.ScoredHit {
	return recall.ScoredHit{
		Hit: recall.Hit{
			SessionID:   id,
			SessionName: name,
			Role:        adapter.RoleUser,
			Created:     time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
			Snippet:     snippet,
		},
		Score: score,
	}
}

func TestRenderPriorConversations_Empty(t *testing.T) {
	if got := renderPriorConversations(nil, 2000); got != "" {
		t.Errorf("empty hits rendered %q, want empty", got)
	}
}

func TestRenderPriorConversations_FormatsLines(t *testing.T) {
	block := renderPriorConversations([]recall.ScoredHit{
		hit("sess-001", "auth-work", "we decided to use jwt", 0.91),
	}, 2000)
	if !strings.Contains(block, "auth-work") {
		t.Errorf("block missing session name: %q", block)
	}
	if !strings.Contains(block, "2026-07-10") {
		t.Errorf("block missing date: %q", block)
	}
	if !strings.Contains(block, "we decided to use jwt") {
		t.Errorf("block missing excerpt: %q", block)
	}
}

func TestRenderPriorConversations_BudgetKeepsTopHit(t *testing.T) {
	hits := []recall.ScoredHit{
		hit("s1", "first", strings.Repeat("x", 300), 0.9),
		hit("s2", "second", strings.Repeat("y", 300), 0.8),
		hit("s3", "third", strings.Repeat("z", 300), 0.7),
	}
	// A tiny budget must still admit the single top hit, and must drop the rest.
	block := renderPriorConversations(hits, 50)
	if !strings.Contains(block, "first") {
		t.Errorf("top hit dropped under tight budget: %q", block)
	}
	if strings.Contains(block, "second") || strings.Contains(block, "third") {
		t.Errorf("over-budget hits not dropped: %q", block)
	}
}

// The injected block must never contain the multi-line section markers that
// extractSummarySection / extractFileRefsBlock scan for, or a later turn would
// mis-bound the preserved summary and file-refs blocks. Excerpts are collapsed
// to single lines upstream, so the body should carry no blank-line boundaries.
func TestRenderPriorConversations_NoSectionMarkerCollision(t *testing.T) {
	block := renderPriorConversations([]recall.ScoredHit{
		hit("s1", "x", "line one line two still one excerpt", 0.9),
	}, 2000)
	if strings.Contains(block, "\n\n") {
		t.Errorf("block contains a blank-line boundary: %q", block)
	}
	if strings.Contains(block, strings.TrimSpace(summaryHeading)) {
		t.Errorf("block contains summary heading marker: %q", block)
	}
	if strings.Contains(block, filerefs.Marker) {
		t.Errorf("block contains file-refs marker: %q", block)
	}
	if strings.TrimSpace(priorConvosHeading) == strings.TrimSpace(summaryHeading) {
		t.Error("priorConvosHeading collides with summaryHeading")
	}
}
