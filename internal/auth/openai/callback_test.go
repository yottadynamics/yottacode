package openai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewCallbackServerValidation(t *testing.T) {
	for _, tc := range []struct {
		uri     string
		wantErr string
	}{
		{"https://127.0.0.1:1455/cb", "must be http"},
		{"http://example.com:1455/cb", "loopback host"},
		{"http://127.0.0.1/cb", "must include a port"},
		{"::not a url::", "parse"},
	} {
		_, err := NewCallbackServer(tc.uri)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("NewCallbackServer(%q) = %v, want error containing %q", tc.uri, err, tc.wantErr)
		}
	}
}

func TestCallbackServerSuccess(t *testing.T) {
	port := freePort(t)
	redirect := "http://127.0.0.1:" + port + "/auth/callback"
	srv, err := NewCallbackServer(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	go func() {
		// Drive the callback. Wait briefly for the listener; net.Listen
		// returned synchronously in Start, so this is effectively a yield.
		q := url.Values{}
		q.Set("code", "AUTHCODE")
		q.Set("state", "S")
		_, _ = http.Get(redirect + "?" + q.Encode())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := srv.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != nil {
		t.Fatalf("res.Err = %v", res.Err)
	}
	if res.Code != "AUTHCODE" || res.State != "S" {
		t.Errorf("got %+v", res)
	}
}

func TestCallbackServerErrorParam(t *testing.T) {
	port := freePort(t)
	redirect := "http://127.0.0.1:" + port + "/auth/callback"
	srv, err := NewCallbackServer(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	go func() {
		q := url.Values{}
		q.Set("error", "access_denied")
		q.Set("error_description", "user said no")
		_, _ = http.Get(redirect + "?" + q.Encode())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := srv.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "user said no") {
		t.Errorf("got err %v, want one containing 'user said no'", res.Err)
	}
}

func TestCallbackServerWaitContextCancel(t *testing.T) {
	port := freePort(t)
	srv, err := NewCallbackServer("http://127.0.0.1:" + port + "/cb")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = srv.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", err)
	}
}

// TestCallbackServerStartPortInUse covers the case the user actually
// hits: the loopback port is already bound (an abandoned sign-in, or a
// second instance). Start must fail with an actionable message that
// names the port, not the raw "bind: address already in use".
func TestCallbackServerStartPortInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, port, err := net.SplitHostPort(occupied.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewCallbackServer("http://127.0.0.1:" + port + "/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err == nil {
		srv.Close()
		t.Fatal("Start should fail when the port is already bound")
	} else {
		for _, want := range []string{"already in use", port, "lsof"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Start error = %q, want it to contain %q", err.Error(), want)
			}
		}
	}
}

// TestCallbackServerStartServeSurvivesCloseNil is the regression for the
// nil-server SIGSEGV. Start's serve goroutine must not read the mutable
// c.server field: if Close nils it before the goroutine is scheduled, a
// field read calls (*http.Server)(nil).Serve and the resulting panic in
// a background goroutine is unrecovered — it crashes the whole test
// binary (in CI it surfaced as an unrelated internal/tui panic). The
// serveHook seam parks the goroutine until the field has been nil'd,
// forcing the losing ordering deterministically; a plain stress loop
// almost never hits this window.
func TestCallbackServerStartServeSurvivesCloseNil(t *testing.T) {
	port := freePort(t)
	redirect := "http://127.0.0.1:" + port + "/auth/callback"
	c, err := NewCallbackServer(redirect)
	if err != nil {
		t.Fatal(err)
	}

	// Park the serve goroutine before it touches the server so the nil
	// assignment below is guaranteed to land first.
	release := make(chan struct{})
	serveHook = func() { <-release }
	t.Cleanup(func() { serveHook = nil })

	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	// Simulate Close winning the race: the field is cleared before the
	// goroutine reads it. Keep a handle to shut the server down, since
	// Close is a no-op once the field is nil.
	srv := c.server
	c.server = nil
	t.Cleanup(func() { _ = srv.Close() })

	// Release the goroutine. Pre-fix it dereferenced the nil field here
	// and SIGSEGV'd; post-fix it serves the captured srv.
	close(release)

	// Prove the server is genuinely serving the captured listener: a real
	// callback still round-trips even though c.server is nil.
	q := url.Values{}
	q.Set("code", "AUTHCODE")
	q.Set("state", "S")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(redirect + "?" + q.Encode())
	if err != nil {
		t.Fatalf("callback request failed — serve goroutine did not survive the close race: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("callback status = %d, want 200", resp.StatusCode)
	}
}

// freePort grabs a random free port by binding to :0 and immediately
// closing. Inevitably racy in theory, fine in practice for a test.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
