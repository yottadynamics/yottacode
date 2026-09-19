package browser

import (
	"context"
	"errors"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

// newSessionWithIDs builds a session with bare trackedPage entries (no
// real *rod.Page — id-only bookkeeping never dereferences .page) so
// untrackPage's index math can be tested directly, fast and
// deterministically, without a real browser.
func newSessionWithIDs(active int, ids ...string) *session {
	pages := make([]*trackedPage, len(ids))
	for i, id := range ids {
		pages[i] = &trackedPage{id: idOf(id)}
	}
	return &session{pages: pages, active: active}
}

func idOf(s string) proto.TargetTargetID { return proto.TargetTargetID(s) }

func activeIDOf(s *session) string {
	if s.active < 0 || s.active >= len(s.pages) {
		return "<out-of-range>"
	}
	return string(s.pages[s.active].id)
}

// TestSession_UntrackPage_EarlierPageRemoved_ActiveFollowsIdentity is the
// regression test for the bug an index-only clamp gets wrong: removing a
// page *before* the active one shifts every later page's index down by
// one, so the active *page* must stay the same even though its index
// changes.
func TestSession_UntrackPage_EarlierPageRemoved_ActiveFollowsIdentity(t *testing.T) {
	// 4 pages so active (index 1, "b") stays in range after the removal
	// purely by index arithmetic — an index-only clamp ("shrink active if
	// it's now out of bounds") wouldn't fire here at all and would leave
	// active=1 pointing at "c" (which shifted into b's old slot), even
	// though b never closed. Needs identity tracking, not index math, to
	// get right.
	s := newSessionWithIDs(1, "a", "b", "c", "d") // active = b
	s.untrackPage(idOf("a"))                      // remove an earlier, inactive page
	if got := activeIDOf(s); got != "b" {
		t.Errorf("active page = %q, want %q (b never closed — active must follow it, not the index)", got, "b")
	}
	if s.active != 0 {
		t.Errorf("active index = %d, want 0 (b shifted down after a's removal)", s.active)
	}
}

// TestSession_UntrackPage_ActivePageRemoved_FallsBackToMostRecent proves
// the active page itself closing (e.g. a popup calling window.close())
// is detected and handled, not left as a stale index silently pointing
// at whatever page shifted into that slot.
func TestSession_UntrackPage_ActivePageRemoved_FallsBackToMostRecent(t *testing.T) {
	s := newSessionWithIDs(1, "a", "b", "c", "d") // active = b
	s.untrackPage(idOf("b"))                      // b (the active page) closes itself
	if got := activeIDOf(s); got != "d" {
		t.Errorf("active page = %q, want %q (fall back to the most recently opened remaining page)", got, "d")
	}
}

func TestSession_UntrackPage_LaterPageRemoved_ActiveUnaffected(t *testing.T) {
	s := newSessionWithIDs(0, "a", "b", "c") // active = a
	s.untrackPage(idOf("c"))
	if got := activeIDOf(s); got != "a" {
		t.Errorf("active page = %q, want %q", got, "a")
	}
	if s.active != 0 {
		t.Errorf("active index = %d, want 0", s.active)
	}
}

// TestSession_UntrackPage_LastPageRemoved_NoPanic covers the degenerate
// case where the only tracked page closes. The session retains a tombstone
// so a concurrent manager liveness check cannot race into an empty registry;
// activePage() will receive the stale page and return the normal CDP error
// rather than panic.
func TestSession_UntrackPage_LastPageRemoved_NoPanic(t *testing.T) {
	s := newSessionWithIDs(0, "a")
	s.untrackPage(idOf("a"))
	if len(s.pages) != 1 {
		t.Errorf("pages = %v, want one tombstone", s.pages)
	}
	if s.active != 0 {
		t.Errorf("active = %d, want 0", s.active)
	}
}

func TestSession_UntrackPage_UnknownIDIsANoop(t *testing.T) {
	s := newSessionWithIDs(1, "a", "b", "c")
	s.untrackPage(idOf("does-not-exist"))
	if len(s.pages) != 3 || s.active != 1 {
		t.Errorf("unrelated untrackPage call mutated state: pages=%v active=%d", s.pages, s.active)
	}
}

// TestSession_CloseTab_RefusesToCloseOnlyRemainingTab and
// TestSession_CloseTab_OutOfRangeIndexErrors both return before touching
// trackedPage.page, so the nil *rod.Page in newSessionWithIDs's fixtures
// is safe here — same reasoning as the untrackPage tests above.
func TestSession_CloseTab_RefusesToCloseOnlyRemainingTab(t *testing.T) {
	s := newSessionWithIDs(0, "a")
	if err := s.closeTab(context.Background(), 0); err == nil {
		t.Fatal("expected an error when closing the only remaining tab")
	}
}

func TestSession_CloseTab_OutOfRangeIndexErrors(t *testing.T) {
	s := newSessionWithIDs(0, "a", "b")
	if err := s.closeTab(context.Background(), 5); !errors.Is(err, ErrTabNotFound) {
		t.Errorf("got %v, want ErrTabNotFound", err)
	}
}

// TestSession_TrackPage_ReturnsCreatedFlag pins the (tp, created)
// contract attachPageCapture's callers rely on to avoid double-attaching
// dialog-dismiss/console/network capture to the same page.
func TestSession_TrackPage_ReturnsCreatedFlag(t *testing.T) {
	s := &session{}
	tp1, created1 := s.trackPage(idOf("a"), nil)
	if !created1 || tp1 == nil {
		t.Fatalf("first trackPage: created=%t tp=%v, want created=true and non-nil", created1, tp1)
	}
	tp2, created2 := s.trackPage(idOf("a"), nil)
	if created2 {
		t.Error("second trackPage for the same id: created=true, want false (dedupe)")
	}
	if tp2 != tp1 {
		t.Error("second trackPage returned a different *trackedPage than the first")
	}
	if len(s.pages) != 1 {
		t.Errorf("trackPage should dedupe by id, got %d entries", len(s.pages))
	}
}

func TestTrackedPage_RecordConsole_EvictsOldestWhenOverCap(t *testing.T) {
	tp := &trackedPage{}
	for i := range maxBufferedEntries + 10 {
		tp.recordConsole(ConsoleEntry{Level: "log", Text: string(rune('a' + i%26))})
	}
	got := tp.consoleSnapshot(0)
	if len(got) != maxBufferedEntries {
		t.Fatalf("buffer len = %d, want %d (should have evicted down to the cap)", len(got), maxBufferedEntries)
	}
	// The oldest 10 entries (i=0..9) must be gone; the buffer should start
	// at what was originally entry index 10.
	if want := string(rune('a' + 10%26)); got[0].Text != want {
		t.Errorf("oldest surviving entry = %q, want %q (FIFO eviction)", got[0].Text, want)
	}
}

func TestTrackedPage_ConsoleSnapshot_RespectsLimit(t *testing.T) {
	tp := &trackedPage{}
	for i := range 5 {
		tp.recordConsole(ConsoleEntry{Text: string(rune('a' + i))})
	}
	got := tp.consoleSnapshot(2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Text != "d" || got[1].Text != "e" {
		t.Errorf("limit should return the most recent entries, got %+v", got)
	}
	// limit<=0 means the full buffer.
	if got := tp.consoleSnapshot(0); len(got) != 5 {
		t.Errorf("limit=0: len = %d, want 5 (full buffer)", len(got))
	}
}

func TestTrackedPage_RecordRequestStart_EvictsOldestWhenOverCap(t *testing.T) {
	tp := &trackedPage{}
	for i := range maxBufferedEntries + 10 {
		tp.recordRequestStart(string(rune('a'+i%26)), "GET", "https://example.com")
	}
	got := tp.networkSnapshot(0)
	if len(got) != maxBufferedEntries {
		t.Fatalf("buffer len = %d, want %d", len(got), maxBufferedEntries)
	}
}

func TestTrackedPage_RecordRequestUpdate_UpdatesInPlace(t *testing.T) {
	tp := &trackedPage{}
	tp.recordRequestStart("req1", "GET", "https://example.com/a")
	tp.recordRequestUpdate("req1", func(e *NetworkEntry) {
		e.Status = 200
		e.StatusText = "OK"
	})
	got := tp.networkSnapshot(0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (update must not create a second entry)", len(got))
	}
	if got[0].Status != 200 || got[0].Method != "GET" || got[0].URL != "https://example.com/a" {
		t.Errorf("unexpected entry after update: %+v", got[0])
	}
}

// TestTrackedPage_RecordRequestUpdate_NoopWhenNotFound is the regression
// test for the design mistake an earlier upsert-with-seed approach would
// have made: a response/failure event for a request whose start was
// never seen (evicted, or missed before Network.enable armed) must not
// create a malformed entry with empty Method/URL.
func TestTrackedPage_RecordRequestUpdate_NoopWhenNotFound(t *testing.T) {
	tp := &trackedPage{}
	tp.recordRequestUpdate("never-started", func(e *NetworkEntry) {
		e.Status = 200
	})
	if got := tp.networkSnapshot(0); len(got) != 0 {
		t.Errorf("networkSnapshot = %+v, want empty (update for an unseen id must be a no-op, not create an entry)", got)
	}
}

func TestTrackedPage_NetworkSnapshot_RespectsLimit(t *testing.T) {
	tp := &trackedPage{}
	for i := range 5 {
		tp.recordRequestStart(string(rune('a'+i)), "GET", "https://example.com")
	}
	got := tp.networkSnapshot(2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].RequestID != "d" || got[1].RequestID != "e" {
		t.Errorf("limit should return the most recent entries, got %+v", got)
	}
}

// snapshot() runs from Status/Handoff after a separate alive() check; if the
// last page closes in between, it must report "nothing", not index an empty
// page registry.
func TestSession_Snapshot_EmptyRegistryDoesNotPanic(t *testing.T) {
	s := newSessionWithIDs(0)
	url, tabs := s.snapshot()
	if url != "" || tabs != 0 {
		t.Errorf("snapshot() on an empty registry = (%q, %d), want (\"\", 0)", url, tabs)
	}
}
