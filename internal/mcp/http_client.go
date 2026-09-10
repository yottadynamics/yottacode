package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPClient connects to an MCP server over HTTP (the streamable
// transport) or SSE, performs the initialize handshake, and proxies
// tools/list + tools/call — the same Client contract StdioClient
// implements, just dialing a URL instead of spawning a subprocess.
// Wraps the official Go SDK's StreamableClientTransport/SSEClientTransport
// — we don't re-implement JSON-RPC framing or the SSE wire format.
type HTTPClient struct {
	// Config inputs (set once, read-only after construction):
	name      string
	url       string
	headers   map[string]string // already $VAR-expanded — see NewHTTPClient
	useSSE    bool
	policy    Policy
	tlsCAFile string

	// warnings holds header $VAR expansion warnings computed at
	// construction time, mirroring StdioClient.Warnings' contract.
	warnings []string

	// Live state (mutated only by Start/Stop, guarded by ops.mu). Mirrors
	// StdioClient's shape exactly — see that file's field comments.
	ops     sessionOps
	stopped bool

	// logs captures connect errors, HTTP status lines, and transport
	// errors — never response bodies — for /mcp logs. Redacted before
	// being recorded; see logf.
	logs *ringBuffer

	// cancelConn cancels the connection's own long-lived context (see
	// Start). Set once Start succeeds; called by Stop to release it.
	cancelConn context.CancelFunc
}

// NewHTTPClient constructs an HTTPClient. The connection is NOT dialed
// here — Start does that. useSSE selects SSEClientTransport (the
// 2024-11-05 spec's transport) over StreamableClientTransport (the
// 2025-03-26 spec's transport, the default); both implement the same
// mcp.Transport interface StdioClient's sdk.CommandTransport does, so
// client.Connect's call shape is identical across all three.
//
// headers may contain $VAR references, expanded here against the process
// environment (os.Expand semantics — an unresolved $VAR becomes the empty
// string, same as stdio's mergeEnv); headerExpansionWarnings surfaces any
// unresolved references via Warnings(). policy and tlsCAFile bound what
// Start is willing to connect to — see Policy and buildTransport.
func NewHTTPClient(name, url string, headers map[string]string, useSSE bool, policy Policy, tlsCAFile string) *HTTPClient {
	warnings := headerExpansionWarnings(name, headers)
	if tlsCAFile != "" {
		if _, err := os.Stat(tlsCAFile); err != nil {
			// Soft warning, not a load-time failure: the file may simply
			// not exist yet at config-load time. Start (via buildTransport)
			// still hard-fails if it's genuinely missing when connecting.
			warnings = append(warnings, fmt.Sprintf("mcp(%s): tls_ca_file %q: %v", name, tlsCAFile, err))
		}
	}
	return &HTTPClient{
		name:      name,
		url:       url,
		headers:   expandHeaders(headers),
		useSSE:    useSSE,
		policy:    policy,
		tlsCAFile: tlsCAFile,
		warnings:  warnings,
		logs:      newRingBuffer(stderrBufLines),
	}
}

// Name returns the configured server name.
func (c *HTTPClient) Name() string { return c.name }

// Warnings returns the (immutable post-construction) list of warnings
// recorded for this client — unresolved $VAR references in the
// configured headers block. Mirrors StdioClient.Warnings.
func (c *HTTPClient) Warnings() []string {
	out := make([]string, len(c.warnings))
	copy(out, c.warnings)
	return out
}

// LogTail returns the last ~200 lines of connect errors, HTTP status
// lines, and transport errors — never response bodies. Satisfies
// mcp.LogSource so /mcp logs works uniformly across transports.
func (c *HTTPClient) LogTail() []string { return c.logs.Lines() }

// logf records a redacted, formatted line to the client's log tail.
func (c *HTTPClient) logf(format string, args ...any) {
	c.logs.push(Redact(fmt.Sprintf(format, args...)))
}

// expandHeaders resolves $VAR references in header values against the
// process environment. Mirrors mergeEnv's os.Expand semantics: an
// unresolved $VAR becomes the empty string, not the literal text. Never
// mutates config — callers keep the literal $VAR string in config.toml;
// only this in-memory copy is expanded.
func expandHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return headers
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = os.Expand(v, os.Getenv)
	}
	return out
}

// headerExpansionWarnings mirrors envExpansionWarnings for HTTP headers:
// one warning per $VAR reference that isn't set in yottacode's process
// environment.
func headerExpansionWarnings(name string, headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	var out []string
	for k, v := range headers {
		for _, varName := range referencedVars(v) {
			if _, set := os.LookupEnv(varName); !set {
				out = append(out, fmt.Sprintf("mcp(%s): header %s references $%s which is unset; header will be sent empty",
					name, k, varName))
			}
		}
	}
	return out
}

// buildTransport constructs a fresh *http.Transport for this client — not
// shared with any other server, and not a fallback to http.DefaultTransport
// (see the doc comment on the old headerTransport.base field this replaces
// as the norm: a nil base silently fell back to the process-wide default
// transport, so every HTTP MCP server shared one connection pool). A short
// ResponseHeaderTimeout bounds a server that accepts the connection but
// never responds; tlsCAFile, if set, adds a custom CA root without
// disabling certificate verification. DialContext's Control hook
// (Policy.DialControl) defeats DNS rebinding of a hostname-configured
// server — see its doc comment.
func (c *HTTPClient) buildTransport() (*http.Transport, error) {
	var host string
	if u, err := url.Parse(c.url); err == nil {
		host = u.Hostname()
	}
	t := &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: (&net.Dialer{
			Control: c.policy.DialControl(host),
		}).DialContext,
	}
	if c.tlsCAFile == "" {
		return t, nil
	}
	pem, err := os.ReadFile(c.tlsCAFile)
	if err != nil {
		return nil, fmt.Errorf("tls_ca_file %q: %w", c.tlsCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tls_ca_file %q: no certificates found", c.tlsCAFile)
	}
	t.TLSClientConfig.RootCAs = pool
	return t, nil
}

// headerTransport injects a fixed set of headers (e.g. an Authorization
// bearer token) into every outgoing request. Neither
// StreamableClientTransport nor SSEClientTransport has a Headers field
// directly — the SDK's own test suite wraps HTTPClient.Transport this
// same way (modelcontextprotocol/go-sdk's mcp/sse_test.go).
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		req = req.Clone(req.Context())
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// Start dials the configured URL and performs the MCP initialize
// handshake. The connect attempt is bounded by InitializeTimeout (or
// the caller-supplied ctx, whichever is shorter) — slow servers don't
// hang the whole session, same policy as StdioClient. Once connected,
// though, the session's own context must NOT inherit that deadline: the
// SDK's SSEClientTransport ties its persistent GET request's entire
// body-read lifetime directly to the ctx passed to Connect (unlike
// StreamableClientTransport, which explicitly detaches internally —
// see modelcontextprotocol/go-sdk's streamable.go). Deriving the
// connection from a context.WithTimeout(ctx, InitializeTimeout) and
// deferring its cancel — the naive mirror of StdioClient's pattern —
// silently kills every SSE session's stream the instant Start returns,
// so CallTool always fails afterward. Racing the connect attempt
// against a timer, using a separate cancel-only context for the
// connection itself, avoids that for both transports.
func (c *HTTPClient) Start(ctx context.Context) error {
	c.ops.mu.Lock()
	if c.ops.started || c.ops.starting {
		c.ops.mu.Unlock()
		return fmt.Errorf("mcp(%s): already started", c.name)
	}
	c.ops.starting = true
	c.ops.mu.Unlock()
	defer func() {
		c.ops.mu.Lock()
		if !c.ops.started {
			c.ops.starting = false
		}
		c.ops.mu.Unlock()
	}()

	if err := c.policy.CheckURL(c.url); err != nil {
		c.logf("policy: %v", err)
		return fmt.Errorf("mcp(%s): %w", c.name, err)
	}

	connCtx, cancelConn := context.WithCancel(context.Background())

	client := sdk.NewClient(&sdk.Implementation{
		Name:    clientName,
		Version: clientVersion(),
	}, nil)

	baseTransport, err := c.buildTransport()
	if err != nil {
		cancelConn()
		c.logf("transport setup: %v", err)
		return fmt.Errorf("mcp(%s): %w", c.name, err)
	}
	httpClient := &http.Client{
		Transport:     &headerTransport{base: baseTransport, headers: c.headers},
		CheckRedirect: c.policy.CheckRedirect,
	}

	var transport sdk.Transport
	if c.useSSE {
		transport = &sdk.SSEClientTransport{Endpoint: c.url, HTTPClient: httpClient}
	} else {
		transport = &sdk.StreamableClientTransport{Endpoint: c.url, HTTPClient: httpClient}
	}

	type connectResult struct {
		session *sdk.ClientSession
		err     error
	}
	resultCh := make(chan connectResult, 1)
	go func() {
		session, err := client.Connect(connCtx, transport, nil)
		resultCh <- connectResult{session, err}
	}()

	var result connectResult
	select {
	case result = <-resultCh:
	case <-time.After(InitializeTimeout):
		cancelConn()
		c.logf("connect: timed out after %s", InitializeTimeout)
		return fmt.Errorf("mcp(%s): connect: timed out after %s", c.name, InitializeTimeout)
	case <-ctx.Done():
		cancelConn()
		c.logf("connect: %v", ctx.Err())
		return fmt.Errorf("mcp(%s): connect: %w", c.name, ctx.Err())
	}
	if result.err != nil {
		cancelConn()
		c.logf("connect: %v", result.err)
		return fmt.Errorf("mcp(%s): connect: %w", c.name, result.err)
	}
	session := result.session

	c.ops.bind(session)
	c.cancelConn = cancelConn

	// Eager catalog fetch — same rationale as StdioClient.Start: the
	// agent registry needs the descriptors at session start anyway, and
	// a tools/list failure surfaces as a startup error here rather than
	// at first tool invocation. Bounded the same way the old code
	// bounded it (InitializeTimeout off ctx) — safe to cancel once this
	// one call returns, since it only governs this request/response
	// round trip, not the underlying persistent connection (connCtx).
	fetchCtx, cancelFetch := context.WithTimeout(ctx, InitializeTimeout)
	_, err = c.ops.fetchTools(fetchCtx)
	cancelFetch()
	if err != nil {
		_ = session.Close()
		cancelConn()
		c.ops.clear()
		c.cancelConn = nil
		c.logf("list tools: %v", err)
		return fmt.Errorf("mcp(%s): list tools: %w", c.name, err)
	}
	return nil
}

// ListTools returns the cached catalog from Start. Returns an error if
// Start hasn't been called or already failed.
func (c *HTTPClient) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	c.ops.mu.RLock()
	defer c.ops.mu.RUnlock()
	return c.ops.listTools()
}

// RefreshTools re-queries tools/list and replaces the cached catalog.
func (c *HTTPClient) RefreshTools(ctx context.Context) ([]ToolDescriptor, error) {
	return c.ops.fetchTools(ctx)
}

// CallTool invokes the named tool with the raw JSON arguments payload.
func (c *HTTPClient) CallTool(ctx context.Context, toolName, argsJSON string) (CallResult, error) {
	return c.ops.callTool(ctx, c.name, toolName, argsJSON)
}

// Stop closes the SDK session (which tears down the HTTP/SSE
// connection) and releases the connection context Start created.
// Idempotent — repeat calls return nil.
func (c *HTTPClient) Stop(ctx context.Context) error {
	c.ops.mu.Lock()
	session := c.ops.session
	cancelConn := c.cancelConn
	alreadyStopped := c.stopped
	c.stopped = true
	c.cancelConn = nil
	c.ops.mu.Unlock()
	c.ops.clear()

	if alreadyStopped || session == nil {
		if cancelConn != nil {
			cancelConn()
		}
		return nil
	}
	if cancelConn != nil {
		cancelConn()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- session.Close() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("mcp(%s): close: timed out", c.name)
	}
}
