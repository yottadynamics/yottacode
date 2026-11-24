package browser

import (
	"context"
	"testing"
	"time"
)

func TestDialogAnswerIsOneShotAndExpires(t *testing.T) {
	now := time.Now()
	a := &dialogAnswer{accept: true, text: "Bob", until: now.Add(time.Minute)}
	if acc, txt, ok := a.take(now); !ok || !acc || txt != "Bob" {
		t.Fatalf("first take = %v %q %v", acc, txt, ok)
	}
	if _, _, ok := a.take(now); ok {
		t.Error("an answer must be consumed by the first dialog")
	}
	b := &dialogAnswer{accept: true, until: now.Add(time.Minute)}
	if _, _, ok := b.take(now.Add(2 * time.Minute)); ok {
		t.Error("an expired answer must not apply")
	}
	if _, _, ok := (*dialogAnswer)(nil).take(now); ok {
		t.Error("nil answer must not apply")
	}
}

func TestAnswerForPolicy(t *testing.T) {
	p := &trackedPage{}
	now := time.Now()
	for _, kind := range []string{"alert", "beforeunload"} {
		if acc, _, by := p.answerFor(kind, now); !acc || by != "automatically" {
			t.Errorf("%s: accept=%v by=%q", kind, acc, by)
		}
	}
	for _, kind := range []string{"confirm", "prompt"} {
		if acc, _, by := p.answerFor(kind, now); acc || by != "automatically" {
			t.Errorf("%s default must cancel: accept=%v by=%q", kind, acc, by)
		}
	}
	// An armed answer applies to confirm/prompt only, and only once.
	p.setDialogArm(&dialogAnswer{accept: true, text: "x", until: now.Add(time.Minute)})
	if acc, _, _ := p.answerFor("alert", now); !acc {
		t.Fatal("alert unaffected")
	}
	if _, _, by := p.answerFor("beforeunload", now); by != "automatically" {
		t.Error("beforeunload must not consume the armed answer")
	}
	if acc, txt, by := p.answerFor("prompt", now); !acc || txt != "x" || by != "by browser_dialog" {
		t.Fatalf("armed prompt: %v %q %q", acc, txt, by)
	}
	if acc, _, _ := p.answerFor("confirm", now); acc {
		t.Error("answer was already used; the next confirm must be cancelled again")
	}
}

func TestArmDialogSharedAcrossTabs(t *testing.T) {
	a, b := &trackedPage{}, &trackedPage{}
	s := &session{pages: []*trackedPage{a, b}}
	if err := s.armDialog(true, "", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if acc, _, _ := a.answerFor("confirm", now); !acc {
		t.Fatal("first tab should take the answer")
	}
	if acc, _, _ := b.answerFor("confirm", now); acc {
		t.Error("the other tab must not reuse a consumed answer")
	}
	_ = s.armDialog(true, "", false)
	_ = s.armDialog(false, "", true) // clear
	if acc, _, by := a.answerFor("confirm", now); acc || by != "automatically" {
		t.Errorf("after clear: %v %q", acc, by)
	}
	if err := (&session{}).armDialog(true, "", false); err == nil {
		t.Error("arming with no page must fail")
	}
}

func TestSettleTuningStateMachine(t *testing.T) {
	p := &trackedPage{}
	const u = "https://x.test/feed?page=1#top"
	net, dom, quick := p.settleBudget(u)
	if quick || net != settleNetworkCap || dom != settleDOMCap {
		t.Fatalf("fresh page must get the full budget: %v %v %v", net, dom, quick)
	}
	p.noteSettle(u, true, false)
	if _, _, quick = p.settleBudget(u); quick {
		t.Fatal("one miss is not enough")
	}
	p.noteSettle(u, true, false)
	net, dom, quick = p.settleBudget("https://x.test/other") // different path: separate page
	if quick {
		t.Fatal("tuning must be per page")
	}
	net, dom, quick = p.settleBudget("https://x.test/feed?page=2") // same page, other query
	if !quick || net != settleQuickNetwork || dom != settleQuickDOM {
		t.Fatalf("two misses should switch to the short budget: %v %v %v", net, dom, quick)
	}
	// A miss in quick mode changes nothing; a clean finish restores the full budget.
	p.noteSettle(u, true, true)
	if _, _, q := p.settleBudget(u); !q {
		t.Fatal("quick-mode miss must not lift quick mode")
	}
	p.noteSettle(u, false, true)
	if _, _, q := p.settleBudget(u); q {
		t.Fatal("a clean settle must restore the full budget")
	}
	// Navigating elsewhere resets.
	p.noteSettle(u, true, false)
	p.noteSettle(u, true, false)
	p.noteSettle("https://y.test/", true, false)
	if _, _, q := p.settleBudget(u); q {
		t.Fatal("a different page must reset the counter")
	}
}

func TestArmedDialogAnswerReachesTabsOpenedLater(t *testing.T) {
	s := &session{pages: []*trackedPage{{}}}
	if err := s.armDialog(true, "", false); err != nil {
		t.Fatal(err)
	}
	late := s.track("late-popup", context.Background(), func() {})
	now := time.Now()
	if acc, _, by := late.answerFor("confirm", now); !acc || by != "by browser_dialog" {
		t.Fatalf("a popup opened after arming must share the answer: %v %q", acc, by)
	}
	// One-shot still holds across tabs: the original tab sees it used.
	if acc, _, _ := s.pages[0].answerFor("confirm", now); acc {
		t.Error("the answer was consumed by the popup")
	}
	// Clearing also reaches later tabs.
	_ = s.armDialog(true, "", false)
	_ = s.armDialog(false, "", true)
	late2 := s.track("late-popup-2", context.Background(), func() {})
	if acc, _, _ := late2.answerFor("confirm", now); acc {
		t.Error("a cleared answer must not reach later tabs")
	}
}
