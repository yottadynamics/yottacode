package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// historyPollInterval is how often back() re-reads the page URL while
// waiting for a history navigation to commit.
const historyPollInterval = 50 * time.Millisecond

// back navigates the active page to the previous entry in its session
// history, like the browser's Back button. It goes through CDP's
// navigation-history entries rather than rod's history.back() shim so a
// page with nothing to go back to is reported (ErrNoHistory) instead of
// silently doing nothing, and so it can wait for the entry to commit.
func (s *session) back(ctx context.Context) (NavigateResult, error) {
	pg := s.activePage().Context(ctx)
	h, err := proto.PageGetNavigationHistory{}.Call(pg)
	if err != nil {
		return NavigateResult{}, fmt.Errorf("browser back: %w", err)
	}
	if h.CurrentIndex <= 0 || h.CurrentIndex >= len(h.Entries) {
		return NavigateResult{}, ErrNoHistory
	}
	target := h.Entries[h.CurrentIndex-1]
	// Entry 0 of a fresh tab is the initial about:blank — not a page the
	// session ever visited, so going "back" to it isn't going back.
	if h.CurrentIndex == 1 && target.URL == "about:blank" {
		return NavigateResult{}, ErrNoHistory
	}
	if err := (proto.PageNavigateToHistoryEntry{EntryID: target.ID}).Call(pg); err != nil {
		return NavigateResult{}, fmt.Errorf("browser back: %w", err)
	}

	// The entry commits asynchronously (and a back/forward-cache restore
	// fires no load event), so wait for the URL to change to the target's
	// rather than for a lifecycle event that may never come.
	tick := time.NewTicker(historyPollInterval)
	defer tick.Stop()
	for {
		info, err := pg.Info()
		if err == nil && info.URL == target.URL {
			if err := pg.WaitLoad(); err != nil {
				return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
			}
			info, err = pg.Info()
			if err != nil {
				return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
			}
			return NavigateResult{URL: info.URL, Title: info.Title}, nil
		}
		select {
		case <-ctx.Done():
			return NavigateResult{}, classifyErr(ctx.Err(), ErrNavigationTimeout)
		case <-tick.C:
		}
	}
}
