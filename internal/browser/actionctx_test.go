package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestActionContextPreservesDeadlineCause(t *testing.T) {
	call, cancelCall := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelCall()
	c, cancel := actionContext(context.Background(), call)
	defer cancel()
	<-c.Done()
	err := ctxFailure(c, "click", errors.New("not visible"))
	if !errors.Is(err, ErrActionTimeout) || errors.Is(err, ErrCanceled) {
		t.Fatalf("deadline must classify as timeout, got %v", err)
	}
}

func TestActionContextExplicitCancel(t *testing.T) {
	call, cancelCall := context.WithCancel(context.Background())
	c, cancel := actionContext(context.Background(), call)
	defer cancel()
	cancelCall()
	<-c.Done()
	if err := ctxFailure(c, "click", errors.New("x")); !errors.Is(err, ErrCanceled) {
		t.Fatalf("explicit cancel must classify as canceled, got %v", err)
	}
}
