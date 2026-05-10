package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// On a fresh session with no memory configured, the startup card
// folds the memory tip in as a dim footer line — the highest-leverage
// thing the user could do next.
func TestWelcome_FreshSessionEmbedsMemoryTipInCard(t *testing.T) {
	m := newTestModel(t)
	if !m.isFreshSession() {
		t.Fatalf("brand-new model should be a fresh session")
	}
	v := m.transcript.String()
	if !strings.Contains(v, "USER.md") {
		t.Errorf("memory tip should mention USER.md: %q", v)
	}
	if !strings.Contains(v, "tip:") {
		t.Errorf("tip prefix should appear inside the startup card: %q", v)
	}
}

func TestWelcome_NotFreshAfterUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
	)
	if m.isFreshSession() {
		t.Errorf("session with a user message is no longer fresh")
	}
}

func TestWelcome_SystemOnlyStillFresh(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleSystem, Content: "you are helpful"},
	)
	if !m.isFreshSession() {
		t.Errorf("system-only session should still be fresh")
	}
}

func TestPickTip_NoMemoryAlwaysReturnsMemoryTip(t *testing.T) {
	for day := 0; day < 366; day++ {
		got := pickTip("", day)
		if got != memoryTip {
			t.Fatalf("day=%d: pickTip(no memory) = %q, want memoryTip", day, got)
		}
	}
}

func TestPickTip_MemoryConfiguredHitsPool(t *testing.T) {
	pool := tipPool()
	if len(pool) == 0 {
		t.Fatalf("tip pool is empty — at least one rotating tip required")
	}
	seen := map[string]bool{}
	for day := 0; day < 366; day++ {
		got := pickTip("USER", day)
		if got == "" {
			t.Fatalf("day=%d: pickTip should never return empty when pool is non-empty", day)
		}
		if got == memoryTip {
			t.Fatalf("day=%d: memory tip should not appear in the rotation pool", day)
		}
		seen[got] = true
	}
	if len(seen) != len(pool) {
		t.Fatalf("rotation visited %d distinct tips over 366 days, want %d", len(seen), len(pool))
	}
}

// Single-command rule: rotating tips that highlight a command should
// reference exactly one (a single backtick-delimited span). Multi-command
// tips dilute the call-to-action. Tips without any backticked span are
// allowed — the prose itself can carry the call-to-action.
func TestTipPool_OneCommandPerTip(t *testing.T) {
	for _, tip := range tipPool() {
		spans := strings.Count(tip, "`") / 2
		// 0 = prose-only tip. 1 = single command. 2 is allowed when the
		// second span is a path or filename being referenced
		// (e.g. ~/.yottacode/memory/).
		if spans > 2 {
			t.Errorf("tip has too many inline-code spans (%d), should reference one command: %q", spans, tip)
		}
	}
}

// startupTip returns the rotating tip body on fresh sessions. With
// memory unset the memory tip wins; with memory set we get one of the
// rotation pool entries.
func TestStartupTip_FreshSessionReturnsTip(t *testing.T) {
	m := newTestModel(t)
	m.memorySummary = ""
	if got := m.startupTip(); !strings.Contains(got, "USER.md") {
		t.Errorf("fresh session w/ no memory should pick the memory tip: %q", got)
	}
	m.memorySummary = "USER+PROJECT"
	if got := m.startupTip(); got == "" || strings.Contains(got, "drop preferences") {
		t.Errorf("with memory loaded the memory tip should rotate out: %q", got)
	}
}

// Resumed (non-fresh) sessions skip the tip entirely — the user is
// past the onboarding moment.
func TestStartupTip_NonFreshReturnsEmpty(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
	)
	if got := m.startupTip(); got != "" {
		t.Errorf("non-fresh session should suppress the tip: %q", got)
	}
}

// shouldShowStartupCard gates the resize-replay card emission. Fresh
// sessions still get the card so the user has context above their
// first prompt; non-fresh sessions don't, since the status bar
// carries the same info.
func TestShouldShowStartupCard_FreshTrueNonFreshFalse(t *testing.T) {
	m := newTestModel(t)
	if !m.shouldShowStartupCard() {
		t.Errorf("fresh session should show the startup card on resize")
	}
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
	)
	if m.shouldShowStartupCard() {
		t.Errorf("non-fresh session should suppress the startup card on resize")
	}
}
