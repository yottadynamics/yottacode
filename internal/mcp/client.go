// Package mcp implements yottacode's client for the Model Context Protocol
// (MCP). yottacode can connect to local stdio subprocess servers, streamable
// HTTP servers, and legacy SSE servers. All transports expose the same Client
// interface so the agent bridge can register their tools under the
// `mcp/<server>/<tool>` namespace without transport-specific branches.
//
// Lifecycle: [Manager] is built from config and started once at session start;
// each healthy client's tools are enumerated and registered in the agent tool
// registry. Failed clients are non-fatal — yottacode keeps running with
// whichever servers came up.
//
// Errors: protocol failures surface as Go errors from CallTool. Tool-level
// errors (the server's own `isError=true` envelope) surface as a non-nil
// [CallResult] with [CallResult.IsError] set; callers translate that to the
// model.
package mcp

import (
	"context"
	"fmt"
)

// Client is the transport-agnostic surface every MCP transport implementation
// exposes to the rest of yottacode.
type Client interface {
	// Name returns the user-configured identifier for this server.
	Name() string

	// Start launches the underlying transport and performs the MCP initialize
	// handshake. After Start returns nil, ListTools and CallTool are safe.
	Start(ctx context.Context) error

	// ListTools returns the cached catalog from Start.
	ListTools(ctx context.Context) ([]ToolDescriptor, error)

	// RefreshTools re-fetches tools/list and replaces the cached catalog. C0
	// callers still use ListTools; C2 live-catalog refresh will call this path.
	RefreshTools(ctx context.Context) ([]ToolDescriptor, error)

	// CallTool invokes one of the server's tools with JSON-encoded arguments.
	CallTool(ctx context.Context, toolName string, argsJSON string) (CallResult, error)

	// Stop releases the underlying transport. Idempotent.
	Stop(ctx context.Context) error
}

// LogSource is implemented by transports that can expose bounded diagnostic
// logs to /mcp logs.
type LogSource interface {
	LogTail() []string
}

// HealthWatcher is the optional C2 liveness hook implemented by transports that
// can report an unexpected disconnect or subprocess death.
type HealthWatcher interface {
	Done() <-chan struct{}
	LastError() error
}

// ToolDescriptor is one tool the server advertised. The fields mirror the
// relevant subset of the MCP Tool schema while keeping callers independent of
// the SDK's concrete type.
type ToolDescriptor struct {
	Name        string
	Title       string
	Description string

	InputSchema  map[string]any
	OutputSchema map[string]any

	ReadOnlyHint bool
	Destructive  bool
	Idempotent   bool
	OpenWorld    bool
}

// CallResult is the agent-bridge-friendly view of CallToolResult.
type CallResult struct {
	Text    string
	IsError bool
}

// ErrNotStarted is returned by Client methods other than Start that are invoked
// before Start succeeds.
var ErrNotStarted = fmt.Errorf("mcp: client not started")
