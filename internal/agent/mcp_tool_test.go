package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/permissions"

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
	// block, when non-nil, makes CallTool wait on it (racing against
	// ctx.Done()) instead of returning immediately — used to simulate a
	// slow server for call-timeout tests.
	block <-chan struct{}
}

func (f *fakeMCPClient) Name() string                                   { return f.name }
func (f *fakeMCPClient) Start(context.Context) error                    { return nil }
func (f *fakeMCPClient) ListTools(context.Context) ([]mcp.ToolDescriptor, error) { return f.tools, nil }
func (f *fakeMCPClient) RefreshTools(context.Context) ([]mcp.ToolDescriptor, error) { return f.tools, nil }
func (f *fakeMCPClient) Stop(context.Context) error                     { return nil }
func (f *fakeMCPClient) CallTool(ctx context.Context, name, args string) (mcp.CallResult, error) {
	f.callCount++
	f.lastTool = name
	f.lastArgs = args
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return mcp.CallResult{}, ctx.Err()
		}
	}
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

func TestMCPTool_RequiresApprovalMatrix(t *testing.T) {
	cases := []struct {
		name         string
		approvalMode string
		trust        bool
		readOnly     bool
		openWorld    bool
		destructive  bool
		want         bool
	}{
		{"ask mode always asks even if read-only+trusted", mcpApprovalAsk, true, true, false, false, true},
		{"allow-readonly + trusted + read-only + closed-world skips", mcpApprovalAllowReadonly, true, true, false, false, false},
		{"allow-readonly but not trusted still asks", mcpApprovalAllowReadonly, false, true, false, false, true},
		{"allow-readonly + trusted but not read-only still asks", mcpApprovalAllowReadonly, true, false, false, false, true},
		{"allow-readonly + trusted + read-only but open-world still asks", mcpApprovalAllowReadonly, true, true, true, false, true},
		{"yolo-inherit + trusted + read-only skips like allow-readonly", mcpApprovalYoloInherit, true, true, false, false, true},
		{
			"no annotations at all (destructive+open-world defaults) always asks " +
				"regardless of trust/approval mode", mcpApprovalAllowReadonly, true, true, true, true, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := &MCPTool{
				Server:           "fs",
				ToolName:         "read_file",
				ReadOnly:         tc.readOnly,
				OpenWorld:        tc.openWorld,
				Destructive:      tc.destructive,
				ApprovalMode:     tc.approvalMode,
				TrustAnnotations: tc.trust,
				Client:           &fakeMCPClient{},
			}
			if got := tool.RequiresApproval(""); got != tc.want {
				t.Errorf("RequiresApproval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewMCPTool_FencesDescription(t *testing.T) {
	td := mcp.ToolDescriptor{Name: "read_file", Description: "Read a file from disk."}
	tool := NewMCPTool("fs", td, &fakeMCPClient{}, mcpApprovalAsk, false, 0)
	want := "[mcp/fs] Read a file from disk."
	if got := tool.Description(); got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestNewMCPTool_EmptyDescriptionGetsPlaceholder(t *testing.T) {
	td := mcp.ToolDescriptor{Name: "read_file", Description: ""}
	tool := NewMCPTool("fs", td, &fakeMCPClient{}, mcpApprovalAsk, false, 0)
	desc := tool.Description()
	if !strings.HasPrefix(desc, "[mcp/fs] ") {
		t.Errorf("Description() = %q, want [mcp/fs]-prefixed placeholder", desc)
	}
	if !strings.Contains(desc, "No description provided") {
		t.Errorf("Description() = %q, want a placeholder noting no description", desc)
	}
}

func TestNewMCPTool_CapsLongDescription(t *testing.T) {
	td := mcp.ToolDescriptor{Name: "read_file", Description: strings.Repeat("x", 5000)}
	tool := NewMCPTool("fs", td, &fakeMCPClient{}, mcpApprovalAsk, false, 0)
	if got := len(tool.Description()); got > maxMCPDescription {
		t.Errorf("Description() length = %d, want <= %d", got, maxMCPDescription)
	}
	if !strings.HasSuffix(tool.Description(), "…") {
		t.Errorf("capped description should end with an ellipsis; got %q", tool.Description())
	}
}

func TestNewMCPTool_CapsOversizedSchema(t *testing.T) {
	bigProps := map[string]any{}
	for i := 0; i < 2000; i++ {
		bigProps[fmt.Sprintf("prop_%04d", i)] = map[string]any{"type": "string"}
	}
	td := mcp.ToolDescriptor{
		Name:        "read_file",
		Description: "Read a file.",
		InputSchema: map[string]any{"type": "object", "properties": bigProps},
	}
	tool := NewMCPTool("fs", td, &fakeMCPClient{}, mcpApprovalAsk, false, 0)
	schema := tool.Schema()
	if schema["type"] != "object" || len(schema) != 1 {
		t.Errorf("oversized schema should be replaced with a generic object schema; got %+v", schema)
	}
	if !strings.Contains(tool.Description(), "Schema exceeded") {
		t.Errorf("Description() should note the schema was replaced; got %q", tool.Description())
	}
}

func TestMCPTool_ExecuteHonorsCallTimeout(t *testing.T) {
	block := make(chan struct{}) // never closed — CallTool blocks until ctx fires
	client := &fakeMCPClient{block: block}
	tool := &MCPTool{
		Server:      "fs",
		ToolName:    "slow_op",
		Client:      client,
		CallTimeout: 20 * time.Millisecond,
	}

	start := time.Now()
	_, err := tool.Execute(context.Background(), `{}`)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error from a call that never returns")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Execute err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Errorf("Execute took %v, want it to return promptly once CallTimeout elapses", elapsed)
	}
}

func TestMCPTool_ExecuteWithoutCallTimeoutIsUnbounded(t *testing.T) {
	// CallTimeout == 0 (its zero value) must not add a timeout — the parent
	// ctx's own cancellation is the only bound. Confirmed by cancelling the
	// parent explicitly and expecting that error, not a spurious internal one.
	client := &fakeMCPClient{block: make(chan struct{})}
	tool := &MCPTool{Server: "fs", ToolName: "slow_op", Client: client}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, `{}`)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Execute err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not return after parent ctx cancellation")
	}
}

func TestGlobAllowCannotCoverDestructiveMCP(t *testing.T) {
	destructive := &MCPTool{Server: "github", ToolName: "delete_repository", Destructive: true}
	nonDestructive := &MCPTool{Server: "github", ToolName: "list_repos", Destructive: false}

	cases := []struct {
		name string
		tool Tool
		rule permissions.Rule
		want bool
	}{
		{"glob rule against destructive tool is refused", destructive, permissions.Rule{Pattern: "github/*"}, true},
		{"exact rule against destructive tool is allowed", destructive, permissions.Rule{Pattern: "github/delete_repository"}, false},
		{"glob rule against non-destructive tool is allowed", nonDestructive, permissions.Rule{Pattern: "github/*"}, false},
		{"non-MCP tool is never refused here", &fakeToolStub{}, permissions.Rule{Pattern: "*"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := globAllowCannotCoverDestructiveMCP(tc.tool, tc.rule); got != tc.want {
				t.Errorf("globAllowCannotCoverDestructiveMCP() = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeToolStub is a minimal non-MCP Tool used only to exercise the
// type-assertion branch of globAllowCannotCoverDestructiveMCP.
type fakeToolStub struct{}

func (fakeToolStub) Name() string                                  { return "stub" }
func (fakeToolStub) Description() string                           { return "" }
func (fakeToolStub) Schema() map[string]any                        { return map[string]any{} }
func (fakeToolStub) RequiresApproval(string) bool                  { return false }
func (fakeToolStub) PreviewCall(string) string                     { return "" }
func (fakeToolStub) Execute(context.Context, string) (string, error) { return "", nil }

// MCPTool must satisfy the agent.Tool interface — guard against future
// signature drift.
var _ Tool = (*MCPTool)(nil)
