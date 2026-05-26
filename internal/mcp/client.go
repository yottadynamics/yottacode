// Package mcp implements yottacode's client for the Model Context
// Protocol (MCP). v1 supports the stdio subprocess transport only —
// HTTP/SSE is a follow-up wedge. The package is structured around a
// transport-agnostic [Client] interface so HTTPClient can land later
// without churning callers.
//
// Lifecycle: [Manager] is built from config and started once at session
// start; each healthy client's tools are then enumerated and registered
// in the agent tool registry under the `mcp/<server>/<tool>` namespace.
// Failed clients are non-fatal — yottacode keeps running with whichever
// servers came up.
//
// Errors: protocol failures surface as Go errors from CallTool. Tool-level
// errors (the server's own `isError=true` envelope) surface as a non-nil
// [CallResult] with [CallResult.IsError] set; callers (the agent tool
// bridge) translate that to the model.
package mcp

import (
	"context"
	"fmt"
)

// Client is the transport-agnostic surface every MCP transport
// implementation exposes to the rest of yottacode. The bridge in
// internal/agent/mcp_tool.go takes a Client (not a *StdioClient) so the
// HTTPClient that lands in v1.1 plugs in without further work.
type Client interface {
	// Name returns the user-configured identifier for this server
	// (matches MCPServer.Name in config). Stable for the client's
	// lifetime; used both for tool-namespace prefixes
	// (`mcp/<Name>/<tool>`) and for /mcp display.
	Name() string

	// Start launches the underlying transport (spawns the subprocess
	// for stdio; opens the HTTPS connection for http) and performs
	// the MCP `initialize` handshake. Returns the first error from
	// either step. After Start returns nil, [ListTools] and
	// [CallTool] are safe to invoke.
	Start(ctx context.Context) error

	// ListTools returns the catalog the server advertised at
	// initialize time. Safe to call repeatedly — implementations
	// cache; the call is cheap.
	ListTools(ctx context.Context) ([]ToolDescriptor, error)

	// CallTool invokes one of the server's tools with the given
	// JSON-encoded arguments. argsJSON is the raw payload the
	// agent loop received from the model — implementations parse it
	// internally.
	//
	// Returns (CallResult, nil) for both successful calls and
	// server-reported tool errors (where CallResult.IsError is true).
	// Returns (zero, err) only for transport-level failures
	// (subprocess died, ctx cancelled, JSON-RPC framing error).
	CallTool(ctx context.Context, toolName string, argsJSON string) (CallResult, error)

	// Stop releases the underlying transport. For stdio: closes
	// stdin, waits up to a few seconds, then sends SIGTERM/SIGKILL
	// (handled by the SDK's CommandTransport.TerminateDuration).
	// Idempotent.
	Stop(ctx context.Context) error
}

// ToolDescriptor is one tool the server advertised. The fields mirror
// the relevant subset of the MCP `Tool` schema — we deliberately don't
// import the SDK's Tool struct here so the rest of yottacode stays
// transport-agnostic.
type ToolDescriptor struct {
	// Name is the server-side tool name (without the `mcp/<server>/`
	// prefix; the bridge prepends that).
	Name string

	// Description is the human-readable hint shown to the model.
	Description string

	// InputSchema is the JSON Schema for the tool's arguments,
	// passed through to the model verbatim. Always a JSON-object
	// value (map[string]any) for SDK-supplied tools.
	InputSchema map[string]any

	// ReadOnlyHint mirrors the MCP `annotations.readOnlyHint` flag.
	// True iff the server explicitly declared the tool read-only;
	// any other case (missing annotations, hint absent) defaults
	// to false so the approval modal fires by default.
	ReadOnlyHint bool
}

// CallResult is the agent-bridge-friendly view of CallToolResult.
// Implementations concatenate the server's text content blocks into
// Text — image/audio/resource content blocks are ignored in v1
// (tools-only scope; multi-modal MCP content lands when the agent loop
// itself learns to forward multi-modal tool results to the model).
type CallResult struct {
	// Text is the concatenated text content from the server, joined
	// with newlines. Empty when the server returned only non-text
	// content (image/audio/resource).
	Text string

	// IsError mirrors the server's CallToolResult.isError flag. The
	// bridge translates IsError=true into a tool_result block with
	// is_error=true so the model self-corrects.
	IsError bool
}

// ErrNotStarted is returned by Client methods other than Start that are
// invoked before Start succeeds. Surfaced verbatim for tests; production
// callers normally hit this only on a transport that failed Start
// silently in a previous call.
var ErrNotStarted = fmt.Errorf("mcp: client not started")
