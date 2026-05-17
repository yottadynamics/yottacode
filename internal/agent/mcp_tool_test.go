package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

// fakeMCPClient is an in-memory test double for mcp.Client.
type fakeMCPClient struct {
	name      string
	tools     []mcp.ToolDescriptor
	result    mcp.CallResult
	err       error
	lastTool  string
	lastArgs  string
	callCount int
}

func (f *fakeMCPClient) Name() string                                   { return f.name }
func (f *fakeMCPClient) Start(context.Context) error                    { return nil }
func (f *fakeMCPClient) ListTools(context.Context) ([]mcp.ToolDescriptor, error) { return f.tools, nil }
func (f *fakeMCPClient) Stop(context.Context) error                     { return nil }
func (f *fakeMCPClient) CallTool(_ context.Context, name, args string) (mcp.CallResult, error) {
	f.callCount++
	f.lastTool = name
	f.lastArgs = args
	if f.err != nil {
		return mcp.CallResult{}, f.err
	}
	return f.result, nil
}

func newTool(client mcp.Client, readOnly bool) *MCPTool {
	return &MCPTool{
		Server:      "fs",
		ToolName:    "read_file",
		Desc:        "Read a file.",
		InputSchema: map[string]any{"type": "object"},
		ReadOnly:    readOnly,
		Client:      client,
	}
}

func TestMCPTool_Name(t *testing.T) {
	tool := newTool(&fakeMCPClient{name: "fs"}, false)
	if got := tool.Name(); got != "mcp/fs/read_file" {
		t.Errorf("Name() = %q, want mcp/fs/read_file", got)
	}
}

func TestMCPTool_RequiresApprovalDefaults(t *testing.T) {
	tool := newTool(&fakeMCPClient{}, false)
	if !tool.RequiresApproval("") {
		t.Error("default tool should require approval")
	}
}

func TestMCPTool_RequiresApprovalReadOnlyHintFlipsOff(t *testing.T) {
	tool := newTool(&fakeMCPClient{}, true)
	if tool.RequiresApproval("") {
		t.Error("read-only-hinted tool should not require approval")
	}
}

func TestMCPTool_ExecuteHappyPath(t *testing.T) {
	client := &fakeMCPClient{result: mcp.CallResult{Text: "file contents"}}
	tool := newTool(client, true)

	got, err := tool.Execute(context.Background(), `{"path":"/x"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "file contents" {
		t.Errorf("result = %q, want %q", got, "file contents")
	}
	if client.lastTool != "read_file" || client.lastArgs != `{"path":"/x"}` {
		t.Errorf("CallTool args = (%q, %q), want (read_file, {\"path\":\"/x\"})",
			client.lastTool, client.lastArgs)
	}
}

func TestMCPTool_ExecuteServerError(t *testing.T) {
	client := &fakeMCPClient{result: mcp.CallResult{Text: "no such file", IsError: true}}
	tool := newTool(client, false)

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error from server isError envelope")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error %q should include server-side text", err)
	}
	if !strings.Contains(err.Error(), "mcp(fs/read_file)") {
		t.Errorf("error %q should namespace the failure", err)
	}
}

func TestMCPTool_ExecuteTransportError(t *testing.T) {
	want := errors.New("subprocess died")
	client := &fakeMCPClient{err: want}
	tool := newTool(client, false)

	_, err := tool.Execute(context.Background(), `{}`)
	if !errors.Is(err, want) {
		t.Errorf("Execute err = %v, want wrap of %v", err, want)
	}
}

func TestMCPTool_ExecuteWithoutClient(t *testing.T) {
	tool := &MCPTool{Server: "fs", ToolName: "read_file"}
	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error when no client bound")
	}
}

func TestMCPTool_PreviewCall(t *testing.T) {
	tool := newTool(&fakeMCPClient{}, false)
	cases := []struct {
		args string
		want string
	}{
		{``, "MCP fs/read_file"},
		{`{}`, "MCP fs/read_file"},
		{`null`, "MCP fs/read_file"},
		{`{"path":"/x"}`, `MCP fs/read_file {"path":"/x"}`},
	}
	for _, tc := range cases {
		if got := tool.PreviewCall(tc.args); got != tc.want {
			t.Errorf("PreviewCall(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestMCPTool_PreviewCallTruncatesLongArgs(t *testing.T) {
	tool := newTool(&fakeMCPClient{}, false)
	long := `{"text":"` + strings.Repeat("x", 500) + `"}`
	got := tool.PreviewCall(long)
	if len(got) >= len(long) {
		t.Errorf("expected preview to truncate; got length %d (input %d)", len(got), len(long))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated preview should end with ellipsis; got %q", got)
	}
}

// MCPTool must satisfy the agent.Tool interface — guard against future
// signature drift.
var _ Tool = (*MCPTool)(nil)
