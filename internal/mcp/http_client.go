package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
	name    string
	url     string
	headers map[string]string
	useSSE  bool

	// Live state (mutated only by Start/Stop, guarded by mu). Mirrors
	// StdioClient's shape exactly — see that file's field comments.
	mu      sync.Mutex
	session *sdk.ClientSession
	started bool
	stopped bool
	tools   []ToolDescriptor

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
func NewHTTPClient(name, url string, headers map[string]string, useSSE bool) *HTTPClient {
	return &HTTPClient{name: name, url: url, headers: headers, useSSE: useSSE}
}

// Name returns the configured server name.
func (c *HTTPClient) Name() string { return c.name }

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
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return fmt.Errorf("mcp(%s): already started", c.name)
	}

	connCtx, cancelConn := context.WithCancel(context.Background())

	client := sdk.NewClient(&sdk.Implementation{
		Name:    clientName,
		Version: clientVersion,
	}, nil)

	httpClient := &http.Client{Transport: &headerTransport{headers: c.headers}}

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
		return fmt.Errorf("mcp(%s): connect: timed out after %s", c.name, InitializeTimeout)
	case <-ctx.Done():
		cancelConn()
		return fmt.Errorf("mcp(%s): connect: %w", c.name, ctx.Err())
	}
	if result.err != nil {
		cancelConn()
		return fmt.Errorf("mcp(%s): connect: %w", c.name, result.err)
	}
	session := result.session

	c.session = session
	c.started = true
	c.cancelConn = cancelConn

	// Eager catalog fetch — same rationale as StdioClient.Start: the
	// agent registry needs the descriptors at session start anyway, and
	// a tools/list failure surfaces as a startup error here rather than
	// at first tool invocation. Bounded the same way the old code
	// bounded it (InitializeTimeout off ctx) — safe to cancel once this
	// one call returns, since it only governs this request/response
	// round trip, not the underlying persistent connection (connCtx).
	fetchCtx, cancelFetch := context.WithTimeout(ctx, InitializeTimeout)
	tools, err := c.fetchTools(fetchCtx)
	cancelFetch()
	if err != nil {
		_ = session.Close()
		cancelConn()
		c.session = nil
		c.started = false
		c.cancelConn = nil
		return fmt.Errorf("mcp(%s): list tools: %w", c.name, err)
	}
	c.tools = tools
	return nil
}

// ListTools returns the cached catalog from Start. Returns an error if
// Start hasn't been called or already failed.
func (c *HTTPClient) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil, ErrNotStarted
	}
	out := make([]ToolDescriptor, len(c.tools))
	copy(out, c.tools)
	return out, nil
}

// fetchTools queries tools/list and translates the SDK's Tool shape
// into our transport-agnostic ToolDescriptor — identical logic to
// StdioClient.fetchTools (duplicated rather than shared: both are small,
// and the two Client implementations otherwise share nothing stateful
// to hang a common helper off of).
func (c *HTTPClient) fetchTools(ctx context.Context) ([]ToolDescriptor, error) {
	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ToolDescriptor, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		schema, _ := toSchemaMap(t.InputSchema)
		readOnly := false
		if t.Annotations != nil {
			readOnly = t.Annotations.ReadOnlyHint
		}
		out = append(out, ToolDescriptor{
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  schema,
			ReadOnlyHint: readOnly,
		})
	}
	return out, nil
}

// CallTool invokes the named tool with the raw JSON arguments payload.
func (c *HTTPClient) CallTool(ctx context.Context, toolName, argsJSON string) (CallResult, error) {
	c.mu.Lock()
	session := c.session
	started := c.started
	c.mu.Unlock()

	if !started || session == nil {
		return CallResult{}, ErrNotStarted
	}

	var args map[string]any
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return CallResult{}, fmt.Errorf("mcp(%s/%s): invalid argument JSON: %w",
				c.name, toolName, err)
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp(%s/%s): %w", c.name, toolName, err)
	}

	return CallResult{
		Text:    extractText(res),
		IsError: res.IsError,
	}, nil
}

// Stop closes the SDK session (which tears down the HTTP/SSE
// connection) and releases the connection context Start created.
// Idempotent — repeat calls return nil.
func (c *HTTPClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	session := c.session
	cancelConn := c.cancelConn
	alreadyStopped := c.stopped
	c.stopped = true
	c.session = nil
	c.started = false
	c.cancelConn = nil
	c.mu.Unlock()

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
