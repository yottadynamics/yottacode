package browser

import "testing"

// Target-created events arrive faster than tabs are set up, so the cap has to
// count tabs still being set up. A real browser can't be made to deliver the
// burst reliably (Chrome's popup blocker allows one window.open per click), so
// the reservation logic is tested directly.
func TestReserveTabCountsTabsStillBeingSetUp(t *testing.T) {
	s := &session{}
	for i := 0; i < MaxTabs; i++ {
		if !s.reserveTab() {
			t.Fatalf("reservation %d of %d refused", i+1, MaxTabs)
		}
	}
	// Nothing is tracked yet (s.pages is empty), exactly as in a burst.
	if s.reserveTab() {
		t.Fatalf("reservation %d accepted although %d tabs are already being set up", MaxTabs+1, MaxTabs)
	}

	s.releaseTab() // one finished setting up
	if !s.reserveTab() {
		t.Error("a freed slot should be reusable")
	}
}

func TestReserveTabCountsTrackedPagesToo(t *testing.T) {
	s := &session{}
	for i := 0; i < MaxTabs-1; i++ {
		s.pages = append(s.pages, &trackedPage{})
	}
	if !s.reserveTab() {
		t.Fatal("the last free slot should be available")
	}
	if s.reserveTab() {
		t.Error("tracked pages plus in-flight reservations must not exceed the cap")
	}
}
