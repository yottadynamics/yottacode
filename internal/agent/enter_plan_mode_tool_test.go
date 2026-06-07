package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestEnterPlanModeTool_Basics(t *testing.T) {
	tool := &EnterPlanModeTool{State: &PlanModeState{}}
	if tool.Name() != "enter_plan_mode" {
		t.Errorf("Name() = %q; want enter_plan_mode", tool.Name())
	}
	if !tool.RequiresApproval(`{}`) {
		t.Errorf("enter_plan_mode must always require approval — the approval IS the mode-flip handshake")
	}
	schema := tool.Schema()
	if props, ok := schema["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Errorf("enter_plan_mode takes no arguments; schema properties = %v", schema["properties"])
	}
	if _, hasRequired := schema["required"]; hasRequired {
		t.Errorf("empty-object schema should not declare required fields")
	}
	if !strings.Contains(tool.Description(), "plan mode") {
		t.Errorf("description should explain plan mode; got %q", tool.Description())
	}
}

// Execute only runs on the approve path, AFTER the TUI flipped the
// shared state and (when a user message existed) resolved the plan
// file — so the success message must point the model at that file.
func TestEnterPlanModeTool_ExecuteReportsPlanFile(t *testing.T) {
	state := &PlanModeState{}
	state.Active.Store(true)
	state.PlanFile = "/home/u/.yottacode/plans/fix-the-cache-deadbeef.md"
	tool := &EnterPlanModeTool{State: state}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, state.PlanFile) {
		t.Errorf("approve message should name the plan file; got %q", out)
	}
	if !strings.Contains(out, "exit_plan_mode") {
		t.Errorf("approve message should point at exit_plan_mode; got %q", out)
	}
}

// Plan file not resolved yet (no user message to slug — defensive
// edge): the message must still be coherent and promise the file on
// the next user message, mirroring the gate's copy.
func TestEnterPlanModeTool_ExecuteWithoutPlanFile(t *testing.T) {
	state := &PlanModeState{}
	state.Active.Store(true)
	tool := &EnterPlanModeTool{State: state}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "next user message") {
		t.Errorf("no-plan-file message should defer to the next user message; got %q", out)
	}
}

func TestIsPlanBoundaryTool(t *testing.T) {
	for _, name := range []string{"enter_plan_mode", "exit_plan_mode"} {
		if !isPlanBoundaryTool(name) {
			t.Errorf("%s should be a plan boundary tool", name)
		}
	}
	for _, name := range []string{"write_file", "run_bash", "todo_write", "git_commit", ""} {
		if isPlanBoundaryTool(name) {
			t.Errorf("%s should NOT be a plan boundary tool", name)
		}
	}
}

// The schema filter is what keeps the model from seeing (or inventing)
// the boundary tools in the wrong mode: exit only while planning,
// enter only while not.
func TestPlanModeSchemaFilter(t *testing.T) {
	on := planModeSchemaFilter(true)
	off := planModeSchemaFilter(false)

	if !on("exit_plan_mode") {
		t.Errorf("exit_plan_mode should be advertised while plan mode is active")
	}
	if off("exit_plan_mode") {
		t.Errorf("exit_plan_mode should be hidden while plan mode is off")
	}
	if on("enter_plan_mode") {
		t.Errorf("enter_plan_mode should be hidden while plan mode is active")
	}
	if !off("enter_plan_mode") {
		t.Errorf("enter_plan_mode should be advertised while plan mode is off")
	}
	for _, name := range []string{"read_file", "write_file", "run_bash", "todo_write"} {
		if !on(name) || !off(name) {
			t.Errorf("%s should pass the filter in both modes", name)
		}
	}
}

// [N] at the enter card steers the model to continue in the current
// mode — the generic "denied by user" would teach it to give up or
// retry the request.
func TestDeniedResultFor_EnterPlanMode(t *testing.T) {
	msg, denied, err := deniedResultFor("enter_plan_mode", true, false, nil)
	if err != nil || !denied {
		t.Fatalf("deniedResultFor: denied=%v err=%v", denied, err)
	}
	if msg != EnterPlanModeRefusalMessage {
		t.Errorf("enter_plan_mode denial should return the tailored refusal; got %q", msg)
	}
	if !strings.Contains(msg, "Continue in the current mode") {
		t.Errorf("refusal must tell the model to continue; got %q", msg)
	}
}

// Regression: yolo must NOT auto-approve the plan-mode boundary tools.
// Before the boundary-tool guard, --yolo + /plan let exit_plan_mode
// ApprovalAuto through: Execute reported "exiting plan mode" while the
// TUI never flipped the shared state — the model then "implemented"
// against a still-active read-only gate, looping forever.
func TestLoop_Yolo_ExitPlanModeStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "exit_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&ExitPlanModeTool{})
	planMode := &PlanModeState{}
	planMode.Active.Store(true)
	planMode.PlanFile = "/tmp/plan.md"
	yolo := &YoloModeState{}
	yolo.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, YoloMode: yolo}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Fatalf("exit_plan_mode under yolo should still raise ApprovalNeeded; got %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.ToolName == "exit_plan_mode" {
			t.Errorf("exit_plan_mode must never auto-approve (source %q)", a.Source)
		}
	}
}

// Same guard, enter side: yolo auto-approving enter_plan_mode would
// tell the model "entered plan mode" while the session state never
// moved — the model then plans with no read-only gate behind it.
func TestLoop_Yolo_EnterPlanModeStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "enter_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	planMode := &PlanModeState{}
	reg.Register(&EnterPlanModeTool{State: planMode})
	yolo := &YoloModeState{}
	yolo.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, YoloMode: yolo}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Fatalf("enter_plan_mode under yolo should still raise ApprovalNeeded; got %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.ToolName == "enter_plan_mode" {
			t.Errorf("enter_plan_mode must never auto-approve (source %q)", a.Source)
		}
	}
	// The denial must carry the tailored steering, not "denied by user".
	var sawRefusal bool
	for _, msg := range hist {
		if msg.Role == adapter.RoleTool && strings.Contains(msg.Content, "user declined entering plan mode") {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Errorf("denied enter_plan_mode should return EnterPlanModeRefusalMessage; history: %+v", hist)
	}
}

// Auto mode is not in the safety floor's way for ordinary edits, but
// the boundary tools must still prompt — auto-approving enter_plan_mode
// with Source=auto-mode has the same broken shape as the yolo case.
func TestLoop_AutoMode_EnterPlanModeStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "enter_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	planMode := &PlanModeState{}
	reg.Register(&EnterPlanModeTool{State: planMode})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Fatalf("enter_plan_mode under auto mode should still raise ApprovalNeeded; got %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.ToolName == "enter_plan_mode" {
			t.Errorf("enter_plan_mode must never auto-approve (source %q)", a.Source)
		}
	}
}

// --yolo sets BypassPermissions alongside YoloMode; the boundary guard
// must hold even when only the bypass flag is set (scripted/CI shapes).
func TestLoop_BypassPermissions_EnterPlanModeStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "enter_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	planMode := &PlanModeState{}
	reg.Register(&EnterPlanModeTool{State: planMode})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, BypassPermissions: true}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	if !hasEvent[ApprovalNeeded](events) {
		t.Fatalf("enter_plan_mode under BypassPermissions should still raise ApprovalNeeded; got %+v", events)
	}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.ToolName == "enter_plan_mode" {
			t.Errorf("enter_plan_mode must never auto-approve (source %q)", a.Source)
		}
	}
}

// Approve path through the loop: the decision callback plays the TUI's
// role (flip state, then AllowOnce) and Execute's success message must
// reflect the post-flip state.
func TestLoop_EnterPlanMode_ApproveExecutesWithFlippedState(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "enter_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	planMode := &PlanModeState{}
	reg.Register(&EnterPlanModeTool{State: planMode})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		// Mirror the TUI's [Y] handler: flip the shared state before
		// forwarding the decision.
		planMode.Active.Store(true)
		planMode.PlanFile = "/tmp/plans/topic.md"
		return AllowOnce
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var sawSuccess bool
	for _, msg := range hist {
		if msg.Role == adapter.RoleTool && strings.Contains(msg.Content, "/tmp/plans/topic.md") {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Errorf("approved enter_plan_mode should report the resolved plan file; history: %+v", hist)
	}
}
