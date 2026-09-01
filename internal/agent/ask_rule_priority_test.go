package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// An explicit Ask rule is the user's standing "always confirm this"
// policy — it must survive every mode overlay, the same way an explicit
// Deny does (see TestLoop_YoloMode_DenyRuleStillWins). Regression for a
// gap where the yolo/auto-mode/plan-mode-allow auto-allow branches were
// checked before the permissions.Ask verdict, silently swallowing an Ask
// rule whenever a mode overlay happened to be active.
func TestLoop_YoloMode_AskRuleStillPrompts(t *testing.T) {
	perms, cwd := permsForTest(t, nil, []string{"Read(.env)"}, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "read_file", ArgsJSON: `{"path":".env"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	// read_file normally auto-executes (requiresApproval=false) — the Ask
	// rule is the only reason this call should ever prompt.
	reg.Register(&mockTool{name: "read_file", requiresApproval: false, output: "secret=1"})
	yoloMode := &YoloModeState{}
	yoloMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 5, YoloMode: yoloMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var sawApprovalNeeded bool
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		sawApprovalNeeded = true
		return AllowOnce
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !sawApprovalNeeded {
		t.Errorf("Ask rule should force a prompt even under --yolo; got events %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "yolo-mode" {
			t.Errorf("yolo-mode auto-allowed a call an Ask rule should have gated: %+v", a)
		}
	}
}

// Symmetric proof for auto mode: an Ask rule on a non-safety-floor tool
// (which /auto would otherwise silently auto-allow) must still prompt.
func TestLoop_AutoMode_AskRuleStillPrompts(t *testing.T) {
	perms, cwd := permsForTest(t, nil, []string{"Write(.env)"}, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":".env","content":"x"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var sawApprovalNeeded bool
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		sawApprovalNeeded = true
		return AllowOnce
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !sawApprovalNeeded {
		t.Errorf("Ask rule should force a prompt even under /auto; got events %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" {
			t.Errorf("auto-mode auto-allowed a call an Ask rule should have gated: %+v", a)
		}
	}
}
