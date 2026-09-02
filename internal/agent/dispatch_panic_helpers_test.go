package agent

import (
	"context"
	"testing"
	"time"
)

// panicContextForDispatchPanicTest forces context.WithCancel to panic after
// runDispatchChild's recovery defer is armed. It avoids passing a literal nil
// Context, which static analysis correctly flags outside this narrow regression
// harness, while still exercising dispatch's orchestration-panic recovery path.
func panicContextForDispatchPanicTest() context.Context {
	return panicOnDoneContext{}
}

type panicOnDoneContext struct{}

func (panicOnDoneContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (panicOnDoneContext) Done() <-chan struct{}       { panic("dispatch panic test context") }
func (panicOnDoneContext) Err() error                  { return nil }
func (panicOnDoneContext) Value(any) any               { return nil }

// withSuppressedPanicRecoveryStderr hides the expected recovered-panic stack from
// successful verbose test output. The tests that use this helper intentionally
// trigger dispatch orchestration panics and assert recovery side effects instead.
func withSuppressedPanicRecoveryStderr(t *testing.T, fn func()) {
	t.Helper()

	setPanicOutputForTest(t, discardPanicOutput{})
	fn()
}

type discardPanicOutput struct{}

func (discardPanicOutput) Write(p []byte) (int, error) { return len(p), nil }
