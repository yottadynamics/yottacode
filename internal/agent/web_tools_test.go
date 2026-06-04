package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchURLTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello from the web"))
	}))
	t.Cleanup(srv.Close)

	tool := &FetchURLTool{Client: srv.Client()}
	out, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"URL: " + srv.URL, "Status: 200", "hello from the web"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestFetchURLTool_BlocksSSRF is a regression for the release follow-up:
// fetch_url auto-executes on a model-supplied URL, so the production
// (default) client must refuse loopback / link-local / private
// destinations — otherwise a prompt-injected URL reaches localhost
// services or cloud-instance metadata.
func TestFetchURLTool_BlocksSSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(srv.Close)

	tool := &FetchURLTool{} // production guarded client (no injected Client)
	_, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err == nil {
		t.Fatal("fetch_url must refuse a loopback URL (SSRF guard)")
	}
	if !strings.Contains(err.Error(), "ssrf guard") {
		t.Errorf("expected an SSRF guard error, got: %v", err)
	}
}

// TestIsNonPublicIP spot-checks the blocked ranges, including the
// 169.254.169.254 cloud-metadata endpoint.
func TestIsNonPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.5", "192.168.1.1", "172.16.0.1", "0.0.0.0"} {
		if !isNonPublicIP(net.ParseIP(s)) {
			t.Errorf("isNonPublicIP(%s) = false, want true (blocked)", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if isNonPublicIP(net.ParseIP(s)) {
			t.Errorf("isNonPublicIP(%s) = true, want false (public)", s)
		}
	}
}

func TestFetchURLTool_RejectsBinaryContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("not really png"))
	}))
	t.Cleanup(srv.Close)

	tool := &FetchURLTool{Client: srv.Client()}
	if _, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`"}`); err == nil {
		t.Fatalf("expected binary content type error")
	}
}

func TestFetchURLTool_TruncatesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	t.Cleanup(srv.Close)

	tool := &FetchURLTool{Client: srv.Client()}
	out, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`","max_bytes":16}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated to 16 bytes") {
		t.Fatalf("expected truncation note:\n%s", out)
	}
}
