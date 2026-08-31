package agent

import (
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// withHistoryLock runs fn while holding cfg.HistoryLock, or directly when
// the lock is nil (oneshot/subagents/tests own their history slice on a
// single goroutine). It serializes the loop's history mutations and
// snapshots against the TUI's concurrent reads of the same shared slice.
// fn must do only in-memory slice work — never a network or channel op —
// so the lock is never held across anything that blocks.
func withHistoryLock(cfg LoopConfig, fn func()) {
	if cfg.HistoryLock != nil {
		cfg.HistoryLock.Lock()
		defer cfg.HistoryLock.Unlock()
	}
	fn()
}

// stampNow sets msg.Timestamp to the current time unless the caller
// already set one — a partial reply preserved from a cancelled stream, for
// instance, keeps whatever time it was built with. This is the single
// choke point every RoleAssistant/RoleTool message appended by the loop
// passes through (directly via appendHistory, or via the sibling
// append*Interrupts/appendToolResults helpers that bypass it but call this
// the same way), so patching new append sites never means remembering to
// add a stamp by hand.
func stampNow(msg adapter.Message) adapter.Message {
	if msg.Timestamp == nil {
		t := time.Now()
		msg.Timestamp = &t
	}
	return msg
}

// appendHistory appends msgs to the history slice under the history lock,
// stamping each with stampNow first.
func appendHistory(cfg LoopConfig, history *[]adapter.Message, msgs ...adapter.Message) {
	for i := range msgs {
		msgs[i] = stampNow(msgs[i])
	}
	withHistoryLock(cfg, func() { *history = append(*history, msgs...) })
}

// snapshotHistory returns a clone of the history taken under the lock —
// safe to read (and hand to the adapter for the whole streaming call)
// without holding the lock across that network work. The element structs
// are copied by value, so a concurrent edit to a message's Content after
// the snapshot can't race this view.
func snapshotHistory(cfg LoopConfig, history *[]adapter.Message) []adapter.Message {
	var out []adapter.Message
	withHistoryLock(cfg, func() { out = slices.Clone(*history) })
	return out
}

// setHistory replaces the whole history slice under the lock (compaction).
func setHistory(cfg LoopConfig, history *[]adapter.Message, next []adapter.Message) {
	withHistoryLock(cfg, func() { *history = next })
}

// panicToError converts a recovered panic value into an error, writing
// the stack to stderr for diagnosis while returning a concise,
// model-safe message. The agent funnels model-driven work through many
// tools, subagents, and background goroutines; a panic in any one of
// them must degrade to a recoverable error rather than an uncaught panic
// that crashes the user's entire session. Call from a deferred closure:
//
//	defer func() {
//		if r := recover(); r != nil {
//			err = panicToError("run_bash", r)
//		}
//	}()
func panicToError(what string, r any) error {
	fmt.Fprintf(os.Stderr, "yottacode: recovered panic in %s: %v\n%s\n", what, r, debug.Stack())
	return fmt.Errorf("%s panicked: %v", what, r)
}
