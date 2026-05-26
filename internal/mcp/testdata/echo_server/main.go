// echo_server is a fixture MCP server used by internal/mcp's stdio
// transport tests. It's a real subprocess (built once at test time and
// invoked via stdio) so the tests exercise the same code path
// production runs hit.
//
// Behavior:
//   - `echo` tool: returns the literal "echo:<text>" content
//   - `crash` tool: os.Exit(2) so tests can verify subprocess-crash
//                   semantics
//   - `slow` tool: sleeps before responding so tests can drive ctx
//                  cancellation
//   - `flaky` tool: returns a tool-level error (CallToolResult.IsError
//                   = true) without a transport-level failure
//
// Two of the tools (`echo` and `slow`) carry the readOnlyHint annotation
// so the bridge tests can verify the approval-gate flip.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text"`
}

type slowArgs struct {
	Ms int `json:"ms"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "echo-server", Version: "test"}, nil)

	readOnlyTrue := true

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Returns the input prefixed with 'echo:'.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnlyTrue},
	}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + args.Text}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "slow",
		Description: "Sleeps for ms milliseconds, then returns 'done'.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnlyTrue},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args slowArgs) (*mcp.CallToolResult, any, error) {
		select {
		case <-time.After(time.Duration(args.Ms) * time.Millisecond):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "flaky",
		Description: "Always returns a tool-level error.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "intentional failure"}},
			IsError: true,
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "crash",
		Description: "Exits the process with code 2.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		os.Exit(2)
		return nil, nil, errors.New("unreachable")
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
