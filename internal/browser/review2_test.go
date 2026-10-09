package browser

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// The documented guarantee is "at most three bytes": a run of continuation
// bytes (a Latin-1 '£' repeated) must not make the loop walk back to nothing.
func TestCutOnRuneNeverBacksUpMoreThanOneCharacter(t *testing.T) {
	run := bytes.Repeat([]byte{0xA3}, 100<<10) // 0xA3 alone is a UTF-8 continuation byte
	got := cutOnRune(run, 64<<10)
	if len(got) < (64<<10)-3 {
		t.Errorf("trimmed to %d of %d: backed up more than %d bytes", len(got), 64<<10, utf8.UTFMax-1)
	}
}

func TestWindowBodyPagesThroughALargeBody(t *testing.T) {
	body := []byte(strings.Repeat("日本語", 5000)) // 45,000 bytes, 3 per character
	var got []byte
	offset, pieces := 0, 0
	for {
		w, next := windowBody(body, offset, 10000)
		if !utf8.Valid(w) {
			t.Fatalf("piece %d is not valid UTF-8", pieces)
		}
		if len(w) > 10000 {
			t.Fatalf("piece %d is %d bytes, over the cap", pieces, len(w))
		}
		got = append(got, w...)
		pieces++
		if next == 0 {
			break
		}
		offset = next
		if pieces > 20 {
			t.Fatal("paging does not terminate")
		}
	}
	if !bytes.Equal(got, body) {
		t.Errorf("reassembled %d bytes, want %d: pieces overlap or drop data", len(got), len(body))
	}
	if pieces < 4 {
		t.Errorf("pieces = %d; a 45 KB body at 10 KB each needs at least 5", pieces)
	}
	if w, next := windowBody(body, len(body)+10, 100); len(w) != 0 || next != 0 {
		t.Errorf("an offset past the end should be empty, got %d bytes next=%d", len(w), next)
	}
	if w, _ := windowBody(body, -5, 6); string(w) != "日本" {
		t.Errorf("negative offset should start at 0, got %q", w)
	}
}

func TestResponseBodyCapMatchesTheOutputCap(t *testing.T) {
	// The tool layer cuts page-derived text at 40,000 characters; a window can
	// never exceed that many bytes, so a single window is never cut twice.
	if MaxResponseBodyBytes > 40000 || DefaultResponseBodyBytes > MaxResponseBodyBytes {
		t.Errorf("caps inconsistent: default=%d max=%d", DefaultResponseBodyBytes, MaxResponseBodyBytes)
	}
}

// One host, many simultaneous requests: one lookup, not one each.
func TestConcurrentRequestsShareOneLookup(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	p := newNetPolicy()
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		calls.Add(1)
		<-release
		return []netip.Addr{netip.MustParseAddr("10.0.0.9")}, nil
	}
	var wg sync.WaitGroup
	results := make([]error, 50)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = p.check("http://slow.example/x")
		}()
	}
	time.Sleep(100 * time.Millisecond) // let them all pile up behind the lookup
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("resolver ran %d times for 50 concurrent requests, want 1", n)
	}
	for i, err := range results {
		if !errors.Is(err, ErrBlockedURL) {
			t.Errorf("request %d got %v; every waiter must see the same private-address verdict", i, err)
		}
	}
}

func TestStalePendingBlockNotesAreForgotten(t *testing.T) {
	s := &session{}
	note := errors.New("blocked")
	// A popup that was closed before it was ever tracked.
	s.onGuardBlock("ghost", "page", "http://x/", note)
	s.mu.Lock()
	s.pendingBlocks["ghost"][0].at = time.Now().Add(-2 * pendingBlockTTL)
	s.mu.Unlock()

	s.onGuardBlock("live", "page", "http://x/", note) // any later note triggers the sweep
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pendingBlocks["ghost"]; ok {
		t.Error("notes for a target that never appeared must expire")
	}
	if len(s.pendingBlocks["live"]) != 1 {
		t.Error("the live target's note must be kept")
	}
}
