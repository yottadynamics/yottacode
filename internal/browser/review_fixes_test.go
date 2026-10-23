package browser

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/target"
)

func TestCutOnRune(t *testing.T) {
	// "é" is 2 bytes: cutting at 1 must back up to 0, never emit half a rune.
	if got := cutOnRune([]byte("éé"), 3); string(got) != "é" {
		t.Errorf("cut mid-rune = %q, want one whole é", got)
	}
	if got := cutOnRune([]byte("hello"), 10); string(got) != "hello" {
		t.Errorf("short body changed: %q", got)
	}
	if got := cutOnRune([]byte("abc"), 2); string(got) != "ab" {
		t.Errorf("ascii cut = %q", got)
	}

	// A Latin-1 page is not valid UTF-8 anywhere. It used to be trimmed byte by
	// byte, quadratically, down to nothing.
	latin1 := bytes.Repeat([]byte{0xE9}, 512<<10) // 'é' in ISO-8859-1
	start := time.Now()
	got := cutOnRune(latin1, 64<<10)
	if d := time.Since(start); d > time.Second {
		t.Errorf("trimming took %s", d)
	}
	if len(got) < (64<<10)-3 {
		t.Errorf("a non-UTF-8 body was trimmed to %d bytes; at most 3 may be dropped", len(got))
	}
	// And for valid text the result is always valid.
	text := bytes.Repeat([]byte("日本語"), 1000)
	for _, n := range []int{1, 2, 3, 4, 5, 100, 101, 102} {
		if out := cutOnRune(text, n); !utf8.Valid(out) {
			t.Errorf("cut at %d produced invalid UTF-8", n)
		}
	}
}

// A lookup that fails says nothing about the name, so it must not be cached as
// "public" for the next 30 seconds.
func TestFailedResolutionIsNotCached(t *testing.T) {
	calls := 0
	p := newNetPolicy()
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary failure")
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
	}
	_ = p.check("http://flaky.example/") // lookup fails: treated as public this once
	if err := p.check("http://flaky.example/"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("the retry resolved to a private address and must be refused: %v", err)
	}
	if calls != 2 {
		t.Errorf("resolver calls = %d, want 2 (the failure must not be remembered)", calls)
	}
}

// The held-notes table must shed NEW targets once full, not wipe the notes of
// targets already waiting.
func TestPendingBlocksCapKeepsExistingNotes(t *testing.T) {
	s := &session{}
	note := errors.New("blocked")
	for i := 0; i < maxPendingBlockTargets; i++ {
		s.onGuardBlock(string(rune('A'+i)), "page", "http://x/", note)
	}
	first := target.ID("A")
	if got := len(s.pendingBlocks[first]); got != 1 {
		t.Fatalf("setup: first target holds %d notes", got)
	}

	s.onGuardBlock("overflow", "page", "http://x/", note) // a 33rd target
	if len(s.pendingBlocks) != maxPendingBlockTargets {
		t.Errorf("table grew past its cap: %d", len(s.pendingBlocks))
	}
	if _, ok := s.pendingBlocks["overflow"]; ok {
		t.Error("a new target past the cap should be dropped")
	}
	s.onGuardBlock("A", "page", "http://x/2", note) // an existing target still records
	if got := len(s.pendingBlocks[first]); got != 2 {
		t.Errorf("existing target's notes = %d, want 2: a full table must not discard them", got)
	}
}
