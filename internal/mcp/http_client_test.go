package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestHTTPServer builds a real in-process MCP server (one "echo"
// tool) and serves it over HTTP (streamable transport) or SSE,
// mirroring internal/mcp/testdata/echo_server's fixture in spirit —
// same tool shape, but in-process over HTTP instead of a stdio
// subprocess, since that's the transport under test here.
func newTestHTTPServer(t *testing.T, useSSE bool) string {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-http-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "echo",
		Description: "Returns the input prefixed with 'echo:'.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *sdk.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + args.Text}},
		}, nil, nil
	})

	getServer := func(*http.Request) *sdk.Server { return srv }
	var handler http.Handler
	if useSSE {
		handler = sdk.NewSSEHandler(getServer, nil)
	} else {
		handler = sdk.NewStreamableHTTPHandler(getServer, nil)
	}

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestHTTPClient_StreamableRoundTrip(t *testing.T) {
	url := newTestHTTPServer(t, false)
	c := NewHTTPClient("test", url, nil, false)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %+v, want one 'echo' tool", tools)
	}
	if !tools[0].ReadOnlyHint {
		t.Error("echo tool should carry ReadOnlyHint")
	}

	res, err := c.CallTool(context.Background(), "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "echo:hi" {
		t.Errorf("CallTool result = %q, want %q", res.Text, "echo:hi")
	}

	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	// Idempotent — a second Stop must not error.
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestHTTPClient_SSERoundTrip(t *testing.T) {
	url := newTestHTTPServer(t, true)
	c := NewHTTPClient("test", url, nil, true)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %+v, want one 'echo' tool", tools)
	}
}

// TestHTTPClient_SSE_CallToolAfterStartReturns is the regression test
// for a bug where Start bound the connection's own context to
// InitializeTimeout and deferred its cancel — fine for the streamable
// transport (which the SDK detaches internally) but fatal for SSE,
// whose persistent GET request ties its entire body-read lifetime
// directly to the ctx passed to Connect. That made every CallTool
// invoked after Start returned fail, because the deferred cancel had
// already torn down the SSE stream the instant Start's stack unwound.
// A short sleep before CallTool reproduces it reliably (the original
// bug fired synchronously, not as a rare race).
func TestHTTPClient_SSE_CallToolAfterStartReturns(t *testing.T) {
	url := newTestHTTPServer(t, true)
	c := NewHTTPClient("test", url, nil, true)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	res, err := c.CallTool(context.Background(), "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallTool after Start returned: %v", err)
	}
	if res.Text != "echo:hi" {
		t.Errorf("CallTool result = %q, want %q", res.Text, "echo:hi")
	}
}

func TestHTTPClient_ListToolsBeforeStartErrors(t *testing.T) {
	c := NewHTTPClient("test", "http://127.0.0.1:0", nil, false)
	if _, err := c.ListTools(context.Background()); err != ErrNotStarted {
		t.Errorf("ListTools before Start = %v, want ErrNotStarted", err)
	}
}

// TestHeaderTransport_InjectsConfiguredHeaders is a direct unit test of
// the header-injection RoundTripper (not a full MCP round-trip — the
// SDK's server side doesn't expose incoming headers back through the
// protocol layer, so asserting on the actual HTTP request is the
// reliable way to test this).
func TestHeaderTransport_InjectsConfiguredHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		gotCustom = req.Header.Get("X-Custom")
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	rt := &headerTransport{base: base, headers: map[string]string{
		"Authorization": "Bearer secret",
		"X-Custom":      "value",
	}}

	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotCustom != "value" {
		t.Errorf("X-Custom header = %q, want %q", gotCustom, "value")
	}
}

func TestHeaderTransport_NoHeadersIsNoOp(t *testing.T) {
	var called bool
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if len(req.Header) != 0 {
			t.Errorf("expected no headers set, got %v", req.Header)
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	rt := &headerTransport{base: base}
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if !called {
		t.Error("base RoundTripper was never called")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
