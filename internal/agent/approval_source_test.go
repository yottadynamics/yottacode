package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// findToolResult returns the RoleTool message answering callID, failing
// the test if none exists — every test below needs to inspect the
// persisted message's ApprovalSource, not just the transient event.
func findToolResult(t *testing.T, hist []adapter.Message, callID string) adapter.Message {
	t.Helper()
	for _, m := range hist {
		if m.Role == adapter.RoleTool && m.ToolCallID == callID {
			return m
		}
	}
	t.Fatalf("no RoleTool message for call %q in history: %+v", callID, hist)
	return adapter.Message{}
}

// TestApprovalSource_YoloMode confirms the auto-approval Source already
// sent as an ApprovalAuto event also lands on the persisted tool_result
// message — previously that string only ever reached the TUI live, and
// was discarded once the loop moved on.
func TestApprovalSource_YoloMode(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hi"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	yoloMode := &YoloModeState{}
	yoloMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, YoloMode: yoloMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := findToolResult(t, hist, "c1").ApprovalSource; got != "yolo-mode" {
		t.Errorf("ApprovalSource = %q, want %q", got, "yolo-mode")
	}
}

// TestApprovalSource_Permissions confirms a matching allow rule stamps
// "permissions", not "yolo-mode" or a blank value.
func TestApprovalSource_Permissions(t *testing.T) {
	perms, cwd := permsForTest(t, []string{"Bash(echo *)"}, nil, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hello"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "x"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := findToolResult(t, hist, "c1").ApprovalSource; got != "permissions" {
		t.Errorf("ApprovalSource = %q, want %q", got, "permissions")
	}
}

// TestApprovalSource_DenyRule confirms a blocked call still carries the
// source that blocked it — "why was this call refused" is exactly the
// kind of thing a troubleshooting log needs, not just "it was refused."
func TestApprovalSource_DenyRule(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Bash(rm *)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /tmp/x"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	cfg := LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		BypassPermissions: true,
		Cwd:               NewCwdRef(cwd), MaxIterations: 5,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	msg := findToolResult(t, hist, "c1")
	if msg.ApprovalSource != "deny-rule" {
		t.Errorf("ApprovalSource = %q, want %q", msg.ApprovalSource, "deny-rule")
	}
}

// TestApprovalSource_UserApprovedAndDenied confirms both outcomes of an
// interactive prompt (AllowOnce and Deny) stamp "user" — deliberately
// coarser than the full Decision enum (see executeToolCallImpl's doc
// comment); the message Content already distinguishes approved from
// denied, ApprovalSource just says a human was asked at all.
func TestApprovalSource_UserApprovedAndDenied(t *testing.T) {
	cases := []struct {
		name     string
		decision Decision
		wantDeny bool
	}{
		{"approved", AllowOnce, false},
		{"denied", Deny, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
				{sseDone("", adapter.ToolCall{ID: "c1", Name: "mut", ArgsJSON: `{}`})},
				{sseToken("ok"), sseDone("ok")},
			}}
			reg := NewRegistry()
			reg.Register(&mockTool{name: "mut", requiresApproval: true, output: "executed"})
			cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
			hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

			if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(_ ApprovalNeeded) Decision {
				return tc.decision
			}); err != nil {
				t.Fatalf("Turn: %v", err)
			}
			msg := findToolResult(t, hist, "c1")
			if msg.ApprovalSource != "user" {
				t.Errorf("ApprovalSource = %q, want %q", msg.ApprovalSource, "user")
			}
			gotDeny := msg.Content == "denied by user"
			if gotDeny != tc.wantDeny {
				t.Errorf("content = %q, denied = %v, want denied = %v", msg.Content, gotDeny, tc.wantDeny)
			}
		})
	}
}
