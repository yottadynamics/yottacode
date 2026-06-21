package openai

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestPendingLoginTimeoutReleasesPort proves the self-heal the inline
// TUI flow relies on: when a sign-in times out (the user walked away
// from the browser), Wait returns the deadline error and the loopback
// port is released, so the next attempt can bind it instead of failing
// with "address already in use".
func TestPendingLoginTimeoutReleasesPort(t *testing.T) {
	port := freePort(t)
	redirect := "http://127.0.0.1:" + port + "/auth/callback"

	pending, err := StartLogin(context.Background(), LoginOptions{
		RedirectURI: redirect,
		OpenBrowser: func(string) error { return nil },
		Timeout:     30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	if _, err := pending.Wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait err = %v, want context.DeadlineExceeded", err)
	}

	// The listener must be gone — a fresh bind on the same port proves
	// the timed-out sign-in released it.
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port %s still held after timeout: %v", port, err)
	}
	l.Close()
}
