package mcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	c := NewHTTPClient("test", url, nil, false, Policy{}, "", nil)
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
	c := NewHTTPClient("test", url, nil, true, Policy{}, "", nil)
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
	c := NewHTTPClient("test", url, nil, true, Policy{}, "", nil)
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
	c := NewHTTPClient("test", "http://127.0.0.1:0", nil, false, Policy{}, "", nil)
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

// TestHTTPClient_SendsProtocolVersionHeader verifies (rather than
// implements) that the pinned SDK sends MCP-Protocol-Version on requests
// after initialization — internal/mcp never sets this header itself.
func TestHTTPClient_SendsProtocolVersionHeader(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-http-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "echo", Description: "x"},
		func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil, nil
		})
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)

	var mu sync.Mutex
	var gotVersion string
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("MCP-Protocol-Version"); v != "" {
			mu.Lock()
			gotVersion = v
			mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)

	c := NewHTTPClient("test", ts.URL, nil, false, Policy{}, "", nil)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.CallTool(context.Background(), "echo", `{}`); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotVersion == "" {
		t.Error("expected the SDK to send MCP-Protocol-Version on a post-initialize request")
	}
}

func TestHTTPClient_BuildTransportIsIsolatedPerClient(t *testing.T) {
	c1 := NewHTTPClient("one", "https://example.com", nil, false, Policy{}, "", nil)
	c2 := NewHTTPClient("two", "https://example.com", nil, false, Policy{}, "", nil)
	t1, err := c1.buildTransport()
	if err != nil {
		t.Fatalf("buildTransport (c1): %v", err)
	}
	t2, err := c2.buildTransport()
	if err != nil {
		t.Fatalf("buildTransport (c2): %v", err)
	}
	if t1 == t2 {
		t.Error("each client should get its own *http.Transport instance, not a shared one")
	}
	var anyTransport http.RoundTripper = t1
	if anyTransport == http.DefaultTransport {
		t.Error("clients must not fall back to the shared http.DefaultTransport")
	}
}

func TestHTTPClient_BuildTransportSetsResponseHeaderTimeout(t *testing.T) {
	c := NewHTTPClient("test", "https://example.com", nil, false, Policy{}, "", nil)
	transport, err := c.buildTransport()
	if err != nil {
		t.Fatalf("buildTransport: %v", err)
	}
	if transport.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 30s", transport.ResponseHeaderTimeout)
	}
}

func TestHTTPClient_BuildTransportRejectsMissingCAFile(t *testing.T) {
	c := NewHTTPClient("test", "https://example.com", nil, false, Policy{}, "/no/such/ca-bundle.pem", nil)
	if _, err := c.buildTransport(); err == nil {
		t.Error("buildTransport should fail when tls_ca_file doesn't exist")
	}
}

func TestHTTPClient_StartRejectsPolicyViolation(t *testing.T) {
	c := NewHTTPClient("test", "http://evil.example/mcp", nil, false, Policy{RequireTLS: true}, "", nil)
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to fail the policy check before ever dialing")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("error should mention policy; got %v", err)
	}
	if len(c.LogTail()) == 0 {
		t.Error("a policy rejection should be recorded in LogTail")
	}
}

func TestHTTPClient_StartFailureRecordsLogLine(t *testing.T) {
	// Port 1 is a privileged, essentially always-closed TCP port — a
	// deterministic, hermetic way to force a fast connection-refused
	// failure without any external network dependency.
	c := NewHTTPClient("test", "http://127.0.0.1:1", nil, false, Policy{}, "", nil)
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail against a closed port")
	}
	if len(c.LogTail()) == 0 {
		t.Error("a connect failure should be recorded in LogTail")
	}
}

func TestNewHTTPClient_ExpandsHeaderVars(t *testing.T) {
	t.Setenv("YOTTACODE_TEST_HTTP_TOKEN", "secret123")
	c := NewHTTPClient("test", "https://example.com", map[string]string{
		"Authorization": "Bearer $YOTTACODE_TEST_HTTP_TOKEN",
	}, false, Policy{}, "", nil)
	if got := c.headers["Authorization"]; got != "Bearer secret123" {
		t.Errorf("expanded header = %q, want %q", got, "Bearer secret123")
	}
}

func TestNewHTTPClient_WarnsOnUnresolvedHeaderVar(t *testing.T) {
	c := NewHTTPClient("test", "https://example.com", map[string]string{
		"Authorization": "Bearer $YOTTACODE_TEST_DEFINITELY_UNSET_HTTP_VAR",
	}, false, Policy{}, "", nil)
	warnings := c.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("Warnings() = %v, want exactly 1", warnings)
	}
	if !strings.Contains(warnings[0], "YOTTACODE_TEST_DEFINITELY_UNSET_HTTP_VAR") {
		t.Errorf("warning should mention the unresolved var; got %q", warnings[0])
	}
	if got := c.headers["Authorization"]; got != "Bearer " {
		t.Errorf("unresolved $VAR should expand to empty string; got %q", got)
	}
}

// newTestTLSServerWithCA builds a real in-process MCP server (one "echo"
// tool) served over HTTPS with a freshly generated, self-signed CA and a
// leaf certificate it issues for 127.0.0.1 — real crypto/x509 material, not
// a stub, so this exercises buildTransport's actual CA-loading path
// (x509.NewCertPool + AppendCertsFromPEM) rather than just its error
// branch. Returns the server URL and the path to the CA's PEM file.
func newTestTLSServerWithCA(t *testing.T) (serverURL, caPath string) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "yottacode-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverCertDER},
		PrivateKey:  serverKey,
	}

	srv := sdk.NewServer(&sdk.Implementation{Name: "test-tls-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "echo", Description: "x"},
		func(_ context.Context, _ *sdk.CallToolRequest, args struct {
			Text string `json:"text"`
		}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + args.Text}}}, nil, nil
		})

	ts := httptest.NewUnstartedServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	caPath = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return ts.URL, caPath
}

func TestHTTPClient_ConnectsWithValidTLSCAFile(t *testing.T) {
	url, caPath := newTestTLSServerWithCA(t)
	c := NewHTTPClient("test", url, nil, false, Policy{}, caPath, nil)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start with a valid tls_ca_file should succeed: %v", err)
	}
	res, err := c.CallTool(context.Background(), "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "echo:hi" {
		t.Errorf("CallTool result = %q, want %q", res.Text, "echo:hi")
	}
}

func TestHTTPClient_RejectsUntrustedCertWithoutCAFile(t *testing.T) {
	url, _ := newTestTLSServerWithCA(t)
	c := NewHTTPClient("test", url, nil, false, Policy{}, "", nil)
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to fail against a self-signed cert when no tls_ca_file trusts its issuer")
	}
	if len(c.LogTail()) == 0 {
		t.Error("the TLS trust failure should be recorded in LogTail")
	}
}

func TestHTTPClient_LogTailRedactsSecrets(t *testing.T) {
	c := NewHTTPClient("test", "https://127.0.0.1:0", nil, false, Policy{}, "", nil)
	c.logf("connect: unexpected header Authorization: Bearer sk-secret-abc123 got status 401")
	lines := c.LogTail()
	if len(lines) != 1 {
		t.Fatalf("LogTail() = %v, want 1 line", lines)
	}
	if strings.Contains(lines[0], "sk-secret-abc123") {
		t.Errorf("LogTail should redact secrets; got %q", lines[0])
	}
}
