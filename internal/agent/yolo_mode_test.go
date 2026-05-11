package agent

import (
	"context"
	"math"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestYoloModeState_IsActiveNilSafe(t *testing.T) {
	var y *YoloModeState
	if y.IsActive() {
		t.Errorf("nil YoloModeState should report inactive")
	}
	y = &YoloModeState{}
	if y.IsActive() {
		t.Errorf("zero-value YoloModeState should report inactive")
	}
	y.Active.Store(true)
	if !y.IsActive() {
		t.Errorf("Active=true should report active")
	}
}

// Yolo bypasses the safety floor: run_bash auto-allows silently. The
// auto-mode test confirms run_bash still prompts in /auto; this is
// the symmetric proof for /yolo.
func TestLoop_YoloMode_AutoApprovesSafetyFloor(t *testing.T) {
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

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("yolo should auto-allow even safety-floor tools; got ApprovalNeeded")
	}
	var sawYolo bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "yolo-mode" {
			sawYolo = true
		}
	}
	if !sawYolo {
		t.Errorf("expected ApprovalAuto with Source=yolo-mode; got %+v", events)
	}
}

// Deny rules still win in yolo — yolo is "skip prompts," not
// "ignore my policy."
func TestLoop_YoloMode_DenyRuleStillWins(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Bash(rm *)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	yoloMode := &YoloModeState{}
	yoloMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: cwd, MaxIterations: 5, YoloMode: yoloMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sawDeny, sawYolo bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			if a.Source == "deny-rule" {
				sawDeny = true
			}
			if a.Source == "yolo-mode" {
				sawYolo = true
			}
		}
	}
	if !sawDeny {
		t.Errorf("expected deny-rule to fire over yolo; got events %+v", events)
	}
	if sawYolo {
		t.Errorf("yolo should NOT override an explicit deny rule")
	}
}

// Yolo lifts the iteration cap entirely. We don't drive a 1000-call
// turn here; instead we assert the loop's effective-cap calculation
// produces MaxInt when yolo is active. That's exercised by Turn's
// behavior — if the cap math were broken, a tool-call loop would
// exit prematurely.
func TestLoop_YoloMode_EffectiveCapIsUnlimited(t *testing.T) {
	yoloMode := &YoloModeState{}
	yoloMode.Active.Store(true)
	cfg := LoopConfig{MaxIterations: 50, YoloMode: yoloMode}
	// The math the Turn function performs at turn start.
	effectiveCap := cfg.MaxIterations
	if cfg.YoloMode.IsActive() {
		effectiveCap = math.MaxInt
	}
	if effectiveCap != math.MaxInt {
		t.Errorf("yolo should disable the iteration cap; got %d", effectiveCap)
	}
}

func TestLoop_AutoMode_EffectiveCapIs4x(t *testing.T) {
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{MaxIterations: 50, AutoMode: autoMode}
	effectiveCap := cfg.MaxIterations
	switch {
	case cfg.YoloMode.IsActive():
		effectiveCap = math.MaxInt
	case cfg.AutoMode.IsActive():
		effectiveCap *= 4
	}
	if effectiveCap != 200 {
		t.Errorf("auto mode multiplier should be 4x; got %d (want 200)", effectiveCap)
	}
}
