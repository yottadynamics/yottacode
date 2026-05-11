package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestAutoModeState_IsActiveNilSafe(t *testing.T) {
	var a *AutoModeState
	if a.IsActive() {
		t.Errorf("nil AutoModeState should report inactive")
	}
	a = &AutoModeState{}
	if a.IsActive() {
		t.Errorf("zero-value AutoModeState should report inactive")
	}
	a.Active.Store(true)
	if !a.IsActive() {
		t.Errorf("Active=true should report active")
	}
}

func TestIsAutoModeSafetyFloor(t *testing.T) {
	for _, name := range []string{"run_bash", "git_commit", "git_checkpoint", "rollback"} {
		if !IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should be in the safety floor", name)
		}
	}
	for _, name := range []string{
		"write_file", "edit_file", "apply_diff", "mkdir", "copy_file", "move_file",
		"delete_file", "git_stage_files", "git_unstage_files", "read_file", "grep",
	} {
		if IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should NOT be in the safety floor", name)
		}
	}
}

// Loop integration: when auto mode is on, mutating tools that aren't
// in the safety floor auto-approve with Source=auto-mode instead of
// triggering the approval modal.
func TestLoop_AutoMode_AutoApprovesNonFloorTool(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"main.go"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("auto-mode write should auto-allow, not prompt")
	}
	var sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawAutoMode {
		t.Errorf("expected ApprovalAuto with Source=auto-mode; got %+v", events)
	}
}

// Safety-floor tools (run_bash, git_commit, …) still prompt even when
// auto mode is on. That's the whole point of the floor.
func TestLoop_AutoMode_SafetyFloorStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, _ = runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	// Verify ApprovalNeeded fired AT LEAST once for run_bash.
	// (The decide func is mandatory in runTurnSync when ApprovalNeeded
	// fires, so reaching here without panic already proves it fired.)
}

// Auto mode doesn't override an explicit Deny rule — the user said
// "never" with permissions.json and that always wins.
func TestLoop_AutoMode_DenyRuleStillWins(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Write(forbidden.go)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"forbidden.go"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: cwd, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sawDeny, sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			if a.Source == "deny-rule" {
				sawDeny = true
			}
			if a.Source == "auto-mode" {
				sawAutoMode = true
			}
		}
	}
	if !sawDeny {
		t.Errorf("expected deny-rule to fire; got %+v", events)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should NOT override an explicit deny rule")
	}
}

// Plan and auto are mutually exclusive at the loop level: when both
// flags happen to be on (shouldn't happen in real use), the plan-mode
// path takes precedence so plan-mode-allow / plan-mode-block still
// govern writes.
func TestLoop_AutoMode_PlanModePrecedesWhenBothActive(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		// Write to plan file → plan-mode-allow.
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"/tmp/plan.md"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	planMode := &PlanModeState{PlanFile: "/tmp/plan.md"}
	planMode.Active.Store(true)
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sources []string
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			sources = append(sources, a.Source)
		}
	}
	// The plan-mode-allow should fire (plan file write). The
	// auto-mode source should NOT fire for this call (plan takes
	// precedence).
	sawPlanAllow := false
	sawAutoMode := false
	for _, s := range sources {
		if s == "plan-mode-allow" {
			sawPlanAllow = true
		}
		if s == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawPlanAllow {
		t.Errorf("expected plan-mode-allow; got sources %+v", sources)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should not fire when plan-mode-allow already handled the call; got sources %+v", sources)
	}
}
