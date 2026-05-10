package agent

import (
	"context"
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

	tool := &FetchURLTool{}
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

func TestFetchURLTool_RejectsBinaryContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("not really png"))
	}))
	t.Cleanup(srv.Close)

	tool := &FetchURLTool{}
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

	tool := &FetchURLTool{}
	out, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`","max_bytes":16}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated to 16 bytes") {
		t.Fatalf("expected truncation note:\n%s", out)
	}
}
