package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

// MCPTool adapts one tool exposed by an MCP server to the agent.Tool
// interface, so MCP tools register into the same Registry as native
// tools and flow through the same approval / permissions / dispatch
// pipeline. Constructed once per (server, tool) pair at session start
// by the manager in internal/tui/run.go.
//
// Naming: the public Name() is the namespaced form `mcp/<Server>/<ToolName>`.
// The unprefixed ToolName is what gets sent over the wire to the
// server's tools/call. The agent loop, model, and permission rules
// only ever see the namespaced form — there's no collision risk with
// native tool names (which never contain a slash).
//
// Approval: defaults to required. The server's `annotations.readOnlyHint`
// hint, when set, flips ReadOnly=true and the bridge stops asking. Users
// can elevate further per-tool via `Tool(mcp/<server>/<tool>)`
// permission rules — same syntax as native tools.
//
// Mutator: MCPTool does NOT implement Mutator. MCP tools mutate state
// outside yottacode's file model (databases, external APIs,
// filesystem-via-server), so /checkpoints can't snapshot them — same
// rationale as run_bash. Document this in docs/mcp.md.
type MCPTool struct {
	// Server is the MCPServer.Name from config. Stable across the
	// session.
	Server string

	// ToolName is the server-side tool name (no namespace prefix).
	ToolName string

	// Desc is the human-readable description the server advertised.
	Desc string

	// InputSchema is the JSON Schema the server advertised, passed
	// through to the model verbatim.
	InputSchema map[string]any

	// ReadOnly mirrors the server's annotations.readOnlyHint. When
	// true, the tool auto-executes without the approval modal.
	ReadOnly bool

	// Client is the live MCP client used to invoke the tool. Held
	// here directly (not looked up by Server name each call) so a
	// /mcp restart of the server replaces the registered tools'
	// Client field via re-registration, not silent indirection.
	Client mcp.Client
}

// Name returns the namespaced tool name: `mcp/<Server>/<ToolName>`.
// Used by the agent loop, model adapter, and permission rules.
func (t *MCPTool) Name() string {
	return "mcp/" + t.Server + "/" + t.ToolName
}

// Description is the server-supplied hint shown to the model.
func (t *MCPTool) Description() string {
	return t.Desc
}

// Schema returns the server-supplied JSON Schema for the tool's
// arguments. The agent loop hands it to the model unchanged.
func (t *MCPTool) Schema() map[string]any {
	if t.InputSchema == nil {
		return map[string]any{}
	}
	return t.InputSchema
}

// RequiresApproval gates the approval modal. The default is "ask";
// only an explicit readOnlyHint=true on the server side flips this
// off. Users can elevate further with permission rules.
//
// Note: argsJSON is unused — MCP tools don't carry per-call policy in
// v1 (no subcommand-style branching the way the unified git tool
// does). Subagents may add finer-grained policy in v1.1+.
func (t *MCPTool) RequiresApproval(string) bool {
	return !t.ReadOnly
}

// PreviewCall renders a single-line summary for the approval modal /
// tool-card header. Format: `MCP <server>/<tool> { ... }`.
// Long argument blobs are truncated.
func (t *MCPTool) PreviewCall(argsJSON string) string {
	args := strings.TrimSpace(argsJSON)
	if args == "" || args == "null" || args == "{}" {
		return fmt.Sprintf("MCP %s/%s", t.Server, t.ToolName)
	}
	const max = 120
	if len(args) > max {
		args = args[:max-1] + "…"
	}
	return fmt.Sprintf("MCP %s/%s %s", t.Server, t.ToolName, args)
}

// Execute calls the underlying MCP tool and translates the result back
// into the agent's (string, error) shape.
//
// Result envelope mapping:
//   - Transport-level failures (subprocess crash, ctx cancellation,
//     malformed argsJSON) surface as a Go error.
//   - Server-side tool errors (CallToolResult.isError = true) surface
//     as a Go error too, with the server's text content as the message
//     — so the agent loop's existing tool_result-with-is_error path
//     fires and the model sees the failure and self-corrects.
//   - Success returns the joined text content as the tool result.
func (t *MCPTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.Client == nil {
		return "", errors.New("mcp tool: no client bound")
	}
	res, err := t.Client.CallTool(ctx, t.ToolName, argsJSON)
	if err != nil {
		return "", err
	}
	if res.IsError {
		msg := strings.TrimSpace(res.Text)
		if msg == "" {
			msg = "tool reported error with no detail"
		}
		return "", fmt.Errorf("mcp(%s/%s): %s", t.Server, t.ToolName, msg)
	}
	return res.Text, nil
}
