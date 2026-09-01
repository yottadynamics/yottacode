package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	godap "github.com/google/go-dap"
)

func TestGoDebugStartMissingDelveReturnsInstallHint(t *testing.T) {
	oldLookPath := debugLookPath
	debugLookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { debugLookPath = oldLookPath })

	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	_, err := manager.startSession(context.Background(), debugStartArgs{Mode: "test", Program: "./pkg"})
	if err == nil {
		t.Fatal("debug_start succeeded without dlv")
	}
	if got := err.Error(); !strings.Contains(got, "dlv not found in PATH") || !strings.Contains(got, "go install github.com/go-delve/delve/cmd/dlv@latest") {
		t.Fatalf("missing install hint: %v", err)
	}
}

func TestGoDebugStartInitializesLaunchesAndEnforcesSingleSession(t *testing.T) {
	client := newFakeGoDebugClient()
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	manager.start = func(context.Context, *CwdRef, debugStartArgs) (*goDebugSession, error) {
		return &goDebugSession{client: client}, nil
	}

	out, err := manager.startSession(context.Background(), debugStartArgs{Mode: "test", Program: "./internal/foo", Args: []string{"-test.run", "TestOne"}})
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if !strings.Contains(out, "mode=test") || !strings.Contains(out, "./internal/foo") {
		t.Fatalf("unexpected start output: %q", out)
	}
	if client.initializeCalls != 1 {
		t.Fatalf("Initialize calls = %d, want 1", client.initializeCalls)
	}
	if got := client.launchArgs["mode"]; got != "test" {
		t.Fatalf("launch mode = %v, want test", got)
	}
	if got := client.launchArgs["program"]; got != "./internal/foo" {
		t.Fatalf("launch program = %v", got)
	}
	if _, err := manager.startSession(context.Background(), debugStartArgs{Mode: "test", Program: "./again"}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second session error = %v, want already running", err)
	}
}

func TestGoDebugStartCleansUpIfContextCancelsAfterLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := newFakeGoDebugClient()
	client.launchHook = cancel
	var canceled bool
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	manager.start = func(context.Context, *CwdRef, debugStartArgs) (*goDebugSession, error) {
		return &goDebugSession{client: client, cancel: func() { canceled = true }}, nil
	}

	_, err := manager.startSession(ctx, debugStartArgs{Mode: "test", Program: "./internal/foo"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("startSession err = %v, want context canceled", err)
	}
	if manager.session != nil {
		t.Fatal("canceled start should clear the active session")
	}
	if client.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want cleanup close", client.closeCalls)
	}
	if !canceled {
		t.Fatal("session cancel func was not called")
	}
}

func TestDebugToolsApprovalPolicy(t *testing.T) {
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	if !(&DebugStartTool{goDebugTool: goDebugTool{manager: manager, name: "debug_start"}}).RequiresApproval(`{}`) {
		t.Fatal("debug_start must require approval")
	}
	if !(&DebugEvalTool{goDebugTool: goDebugTool{manager: manager, name: "debug_eval"}}).RequiresApproval(`{}`) {
		t.Fatal("debug_eval must require approval")
	}
	if (&DebugStackTool{goDebugTool: goDebugTool{manager: manager, name: "debug_stack"}}).RequiresApproval(`{}`) {
		t.Fatal("debug_stack should not require approval after session start")
	}
	if (&DebugVarsTool{goDebugTool: goDebugTool{manager: manager, name: "debug_vars"}}).RequiresApproval(`{}`) {
		t.Fatal("debug_vars should not require approval after session start")
	}
}

func TestGoDebugContinueReportsStillRunning(t *testing.T) {
	client := newFakeGoDebugClient()
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	manager.session = &goDebugSession{client: client}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, err := (&DebugContinueTool{goDebugTool: goDebugTool{manager: manager, name: "debug_continue"}}).Execute(ctx, `{}`)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("continue err = %v, want context deadline", err)
	}
	if out != "" {
		t.Fatalf("continue output = %q, want empty on context cancellation", out)
	}
}

func TestGoDebugWaitForStopReportsStillRunning(t *testing.T) {
	client := newFakeGoDebugClient()
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	manager.session = &goDebugSession{client: client}

	out, err := manager.waitForStop(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatalf("waitForStop: %v", err)
	}
	if out != "still running after 30s" {
		t.Fatalf("waitForStop = %q", out)
	}
}

func TestGoDebugStackVarsAndEvalUseDAP(t *testing.T) {
	client := newFakeGoDebugClient()
	client.stack = godap.StackTraceResponseBody{StackFrames: []godap.StackFrame{{Id: 42, Name: "TestThing", Source: &godap.Source{Path: "thing_test.go"}, Line: 12, Column: 3}}}
	client.scopes = godap.ScopesResponseBody{Scopes: []godap.Scope{{Name: "Globals", VariablesReference: 1}, {Name: "Locals", VariablesReference: 9}}}
	client.variables = godap.VariablesResponseBody{Variables: []godap.Variable{{Name: "got", Value: "nil", Type: "error"}}}
	client.evaluate = godap.EvaluateResponseBody{Type: "int", Result: "42"}
	manager := newGoDebugManager(NewCwdRef(t.TempDir()))
	manager.session = &goDebugSession{client: client}
	manager.threadID = 7

	stack, err := (&DebugStackTool{goDebugTool: goDebugTool{manager: manager, name: "debug_stack"}}).Execute(context.Background(), `{}`)
	if err != nil || !strings.Contains(stack, "#42 TestThing thing_test.go:12:3") {
		t.Fatalf("stack = %q, err=%v", stack, err)
	}
	vars, err := (&DebugVarsTool{goDebugTool: goDebugTool{manager: manager, name: "debug_vars"}}).Execute(context.Background(), `{"frame":42,"filter":"local"}`)
	if err != nil || !strings.Contains(vars, "got = nil (error)") {
		t.Fatalf("vars = %q, err=%v", vars, err)
	}
	if client.lastVariablesReference != 9 {
		t.Fatalf("variables reference = %d, want locals ref 9", client.lastVariablesReference)
	}
	eval, err := (&DebugEvalTool{goDebugTool: goDebugTool{manager: manager, name: "debug_eval"}}).Execute(context.Background(), `{"expression":"answer","frame":42}`)
	if err != nil || !strings.Contains(eval, "int\n42") {
		t.Fatalf("eval = %q, err=%v", eval, err)
	}
	if client.lastEvaluateExpression != "answer" || client.lastEvaluateFrame != 42 {
		t.Fatalf("evaluate args = %q frame=%d", client.lastEvaluateExpression, client.lastEvaluateFrame)
	}
}

func TestRegisterCoreCwdToolsRegistersGoDebugTools(t *testing.T) {
	reg := NewRegistry()
	RegisterCoreCwdTools(reg, NewCwdRef(t.TempDir()), CoreToolDeps{})
	for _, name := range []string{"debug_start", "debug_breakpoint", "debug_continue", "debug_step", "debug_stack", "debug_vars", "debug_eval", "debug_stop"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("tool %s not registered", name)
		}
	}
}

type fakeGoDebugClient struct {
	initializeCalls        int
	launchArgs             map[string]any
	continueCalls          int
	lastVariablesReference int
	lastEvaluateExpression string
	lastEvaluateFrame      int
	stack                  godap.StackTraceResponseBody
	scopes                 godap.ScopesResponseBody
	variables              godap.VariablesResponseBody
	evaluate               godap.EvaluateResponseBody
	launchHook             func()
	closeCalls             int
	events                 chan godap.EventMessage
}

func newFakeGoDebugClient() *fakeGoDebugClient {
	return &fakeGoDebugClient{events: make(chan godap.EventMessage)}
}

func (f *fakeGoDebugClient) Initialize(context.Context, godap.InitializeRequestArguments) (godap.Capabilities, error) {
	f.initializeCalls++
	return godap.Capabilities{}, nil
}
func (f *fakeGoDebugClient) Launch(_ context.Context, args map[string]any) error {
	f.launchArgs = args
	if f.launchHook != nil {
		f.launchHook()
	}
	return nil
}
func (f *fakeGoDebugClient) SetBreakpoints(context.Context, godap.SetBreakpointsArguments) (godap.SetBreakpointsResponseBody, error) {
	return godap.SetBreakpointsResponseBody{Breakpoints: []godap.Breakpoint{{Verified: true, Line: 12}}}, nil
}
func (f *fakeGoDebugClient) ConfigurationDone(context.Context) error { return nil }
func (f *fakeGoDebugClient) Continue(context.Context, godap.ContinueArguments) (godap.ContinueResponseBody, error) {
	f.continueCalls++
	return godap.ContinueResponseBody{AllThreadsContinued: true}, nil
}
func (f *fakeGoDebugClient) Next(context.Context, godap.NextArguments) error       { return nil }
func (f *fakeGoDebugClient) StepIn(context.Context, godap.StepInArguments) error   { return nil }
func (f *fakeGoDebugClient) StepOut(context.Context, godap.StepOutArguments) error { return nil }
func (f *fakeGoDebugClient) StackTrace(context.Context, godap.StackTraceArguments) (godap.StackTraceResponseBody, error) {
	return f.stack, nil
}
func (f *fakeGoDebugClient) Scopes(context.Context, godap.ScopesArguments) (godap.ScopesResponseBody, error) {
	return f.scopes, nil
}
func (f *fakeGoDebugClient) Variables(_ context.Context, args godap.VariablesArguments) (godap.VariablesResponseBody, error) {
	f.lastVariablesReference = args.VariablesReference
	return f.variables, nil
}
func (f *fakeGoDebugClient) Evaluate(_ context.Context, args godap.EvaluateArguments) (godap.EvaluateResponseBody, error) {
	f.lastEvaluateExpression = args.Expression
	f.lastEvaluateFrame = args.FrameId
	return f.evaluate, nil
}
func (f *fakeGoDebugClient) Disconnect(context.Context, godap.DisconnectArguments) error { return nil }
func (f *fakeGoDebugClient) Events() <-chan godap.EventMessage                           { return f.events }
func (f *fakeGoDebugClient) Close(context.Context) error {
	f.closeCalls++
	return nil
}
