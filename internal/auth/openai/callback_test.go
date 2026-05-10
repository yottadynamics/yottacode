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
