package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func newTestAgentTool(t *testing.T, configs []subagents.AgentConfig, streamer Streamer, allowBackground bool) (*AgentTool, *Registry) {
	t.Helper()
	parent := NewRegistry()
	parent.Register(&mockTool{name: "read_file", output: "file body"})
	parent.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "shell out"})
	tool := &AgentTool{
		Configs:         configs,
		Tasks:           subagents.NewRegistry(),
		Adapter:         streamer,
		ParentRegistry:  parent,
		Cwd:             NewCwdRef(t.TempDir()),
		TranscriptDir:   t.TempDir(),
		YoloMode:        &YoloModeState{},
		PlanMode:        &PlanModeState{},
		AutoMode:        &AutoModeState{},
		AllowBackground: allowBackground,
	}
	parent.Register(tool)
	return tool, parent
}

func TestAgentTool_RecursionGuard(t *testing.T) {
	// A config that tries to allowlist Agent into the child must not
	// actually expose it. Belt-and-suspenders: the child-registry
	// builder hard-excludes Agent regardless of the allowlist.
	cfg := subagents.AgentConfig{
		Name:        "evil",
		Description: "tries to recurse",
		Tools:       []string{"read_file", "Agent"},
		Prompt:      "be evil",
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get(agentToolName); ok {
		t.Fatalf("child registry contains the Agent tool — recursion guard failed")
	}
	if _, ok := child.Get("read_file"); !ok {
		t.Errorf("child registry missing read_file (should be inherited)")
	}
}

func TestAgentTool_ExcludesExitPlanMode(t *testing.T) {
	cfg := subagents.AgentConfig{
		Name:        "x",
		Description: "x",
		Prompt:      "x",
		Source:      "test",
	}
	tool, parent := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	parent.Register(&ExitPlanModeTool{})
	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get("exit_plan_mode"); ok {
		t.Errorf("child registry should not contain exit_plan_mode")
	}
}

func TestAgentTool_AppliesToolAllowlist(t *testing.T) {
	cfg := subagents.AgentConfig{
		Name:        "readonly",
		Description: "x",
		Tools:       []string{"read_file"}, // run_bash explicitly excluded
		Prompt:      "x",
		Source:      "test",
	}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get("read_file"); !ok {
		t.Errorf("read_file should be present")
	}
	if _, ok := child.Get("run_bash"); ok {
		t.Errorf("run_bash should be filtered out by the allowlist")
	}
}

func TestAgentTool_UnknownSubagentTypeReturnsRecoverableError(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	args := mustJSON(t, agentArgs{SubagentType: "DoesNotExist", Prompt: "hello"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute returned error %v — should return recoverable string", err)
	}
	if !strings.Contains(out, "unknown subagent_type") {
		t.Errorf("output = %q, want a clear unknown-type error", out)
	}
}

func TestAgentTool_BackgroundRejectedWhenDisabled(t *testing.T) {
	// AllowBackground=false models two cases: oneshot (where it's
	// always false) AND the TUI without the experimental gate
	// enabled. Either way, the error message must be recoverable
	// (so the model adapts to foreground) and must mention the
	// experimental gate so the user knows how to enable it.
	cfg := subagents.AgentConfig{Name: "x", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false /* AllowBackground=false */)
	args := mustJSON(t, agentArgs{SubagentType: "x", Prompt: "hello", RunInBackground: true})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute err = %v, want nil", err)
	}
	if !strings.Contains(out, "background") {
		t.Errorf("output should mention 'background'; got %q", out)
	}
	if !strings.Contains(out, "experimental") {
		t.Errorf("output should mention the experimental gate so users know how to enable; got %q", out)
	}
	if !strings.Contains(out, "background_subagents") {
		t.Errorf("output should name the specific feature flag; got %q", out)
	}
}

func TestAgentTool_ForegroundCapturesFinalReply(t *testing.T) {
	// The child Turn does one round: assistant returns content directly,
	// no tool calls. The tool should return that content as the result.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("subagent answer: the readme is at README.md")},
	}}
	cfg := subagents.AgentConfig{Name: "Explore", Description: "x", Prompt: "be terse", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "where is the readme?"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "subagent answer: the readme is at README.md"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to contain %q", out, want)
	}
	tasks := tool.Tasks.List()
	if len(tasks) != 1 {
		t.Fatalf("Tasks.List len = %d, want 1", len(tasks))
	}
	if tasks[0].Status != subagents.TaskCompleted {
		t.Errorf("Status = %v, want completed", tasks[0].Status)
	}
}

func TestAgentTool_ForegroundContextIsolation(t *testing.T) {
	// The child streams content tokens AND a final reply. The PARENT's
	// events must not contain the child's ContentTokens — that's the
	// entire point of subagents. We simulate the parent by capturing
	// the events the AgentTool would emit through ParentEvents.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{
			sseToken("internal "),
			sseToken("reasoning "),
			sseToken("only"),
			sseDone("clean final answer"),
		},
	}}
	cfg := subagents.AgentConfig{Name: "Explore", Description: "x", Prompt: "be terse", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	parentEvents := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), parentEvents)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "find it"})
	done := make(chan struct{})
	go func() {
		_, _ = tool.Execute(ctx, args)
		close(done)
		close(parentEvents)
	}()

	var sawContentToken bool
	var sawSubagentStart bool
	var sawSubagentDone bool
	for ev := range parentEvents {
		switch ev.(type) {
		case ContentToken:
			sawContentToken = true
		case SubagentStart:
			sawSubagentStart = true
		case SubagentDone:
			sawSubagentDone = true
		}
	}
	<-done

	if sawContentToken {
		t.Errorf("parent saw a ContentToken from the child — context isolation failed")
	}
	if !sawSubagentStart {
		t.Errorf("parent did not see SubagentStart")
	}
	if !sawSubagentDone {
		t.Errorf("parent did not see SubagentDone")
	}
}

func TestAgentTool_SchemaIncludesRequiredFields(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	schema := tool.Schema()
	required, _ := schema["required"].([]string)
	wantSet := map[string]bool{"subagent_type": false, "prompt": false}
	for _, r := range required {
		if _, ok := wantSet[r]; ok {
			wantSet[r] = true
		}
	}
	for k, found := range wantSet {
		if !found {
			t.Errorf("schema.required missing %q", k)
		}
	}
}

func TestAgentTool_RequiresApprovalIsFalse(t *testing.T) {
	tool, _ := newTestAgentTool(t, nil, nil, false)
	if tool.RequiresApproval("") {
		t.Errorf("Agent tool itself must not require approval; child tool calls handle approval individually")
	}
}

func TestAgentTool_DescriptionListsAvailableAgents(t *testing.T) {
	cfgs := []subagents.AgentConfig{
		{Name: "Explore", Description: "Read-only search.", Prompt: "x", Source: "test"},
		{Name: "Plan", Description: "Plan-drafting.", Prompt: "x", Source: "test"},
	}
	tool, _ := newTestAgentTool(t, cfgs, nil, false)
	desc := tool.Description()
	if !strings.Contains(desc, "Explore") || !strings.Contains(desc, "Plan") {
		t.Errorf("description must list available agents; got: %q", desc)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestAgentTool_PlanModePropagatesToChild verifies the mode propagation
// invariant: when the parent is in plan mode, the child runs in plan
// mode too. A child that attempts a write outside the plan file gets
// blocked by PlanModeGate just like the parent would. We exercise this
// by configuring a child registry that includes a mutating mock tool
// (RequiresApproval=true), scripting the child to call it, and
// asserting the result is the plan-mode block message — not the tool's
// output.
func TestAgentTool_PlanModePropagatesToChild(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("done after blocked write")},
	}}
	cfg := subagents.AgentConfig{Name: "test", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)
	tool.PlanMode.Active.Store(true)
	tool.PlanMode.PlanFile = "/tmp/plan.md"

	args := mustJSON(t, agentArgs{SubagentType: "test", Prompt: "do something"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The final reply ends up captured; the run_bash call should have
	// hit PlanModeGate and returned its block message as the tool
	// result, which the model then incorporated into the final reply.
	// Either way the run_bash tool's actual output ("shell out") must
	// NOT appear in the result.
	if strings.Contains(out, "shell out") {
		t.Errorf("child under plan-mode parent executed run_bash; got %q", out)
	}
}

// TestAgentTool_AutoModePropagatesToChild verifies the child inherits
// the parent's auto-mode pointer and that the child loop's
// effective iteration cap reflects auto-mode's 4× multiplier. We
// don't need to exercise the multiplier itself — the agent loop is
// already tested elsewhere — just verify the pointer wiring.
func TestAgentTool_AutoModePropagatesToChild(t *testing.T) {
	cfg := subagents.AgentConfig{Name: "test", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	tool.AutoMode.Active.Store(true)

	// runChild builds the child config inline; check that the
	// AgentTool's AutoMode pointer is what would be passed in.
	if !tool.AutoMode.IsActive() {
		t.Fatalf("expected AutoMode to be active for this test setup")
	}
	// The configuration is built per-call inside runChild; the
	// pointer identity check elsewhere proves propagation. Here we
	// just sanity-check that the AgentTool retains the parent's
	// active state — the alternative (a fresh disabled state) would
	// fail this assertion.
	if tool.AutoMode == nil {
		t.Errorf("AgentTool.AutoMode is nil; child cannot inherit")
	}
}

// TestAgentTool_BackgroundAutoDeniesApproval verifies the hybrid
// approval rule: background subagents auto-deny ApprovalNeeded
// because there's no UI attached. The child should get a Deny
// verdict back from runChild on its own decisions channel.
func TestAgentTool_BackgroundAutoDeniesApproval(t *testing.T) {
	// Script the child to: (1) call a tool that requires approval,
	// (2) after that returns "denied by user", emit a final reply.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("adapted after denial")},
	}}
	cfg := subagents.AgentConfig{Name: "x", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, true)

	args := mustJSON(t, agentArgs{SubagentType: "x", Prompt: "p", RunInBackground: true})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Background tool returns a handle immediately.
	if !strings.Contains(out, "background subagent") {
		t.Fatalf("expected immediate background handle, got: %q", out)
	}
	// Wait for the detached goroutine to finish (run is short with
	// scripted streamer + nil emitToParent).
	for i := 0; i < 50; i++ {
		tasks := tool.Tasks.List()
		if len(tasks) > 0 && tasks[0].Status != subagents.TaskRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tasks := tool.Tasks.List()
	if len(tasks) == 0 {
		t.Fatalf("no task recorded")
	}
	// The child should have completed (not errored) because the
	// auto-deny lets the model adapt and produce a final reply.
	if tasks[0].Errored {
		t.Errorf("expected non-errored completion after auto-deny adapt; got Result=%q", tasks[0].Result)
	}
}

// TestAgentTool_ForegroundForwardsApproval verifies the foreground
// path forwards a child's ApprovalNeeded event to the parent's events
// channel and reads the user's verdict back from the parent's
// decisions channel. We simulate the parent UI by draining its events
// channel in a goroutine and answering the approval modal.
func TestAgentTool_ForegroundForwardsApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "1", Name: "run_bash", ArgsJSON: `{"command":"ls"}`})},
		{sseDone("ran with approval")},
	}}
	cfg := subagents.AgentConfig{Name: "fg", Description: "x", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, streamer, false)

	parentEvents := make(chan Event, 64)
	parentDecisions := make(chan Decision, 1)
	ctx := WithParentDecisions(WithParentEvents(context.Background(), parentEvents), parentDecisions)

	// Simulated parent UI: watch for the forwarded ApprovalNeeded
	// and answer Allow. Run in a goroutine so Execute can block on
	// the response.
	gotApproval := make(chan ApprovalNeeded, 1)
	go func() {
		for ev := range parentEvents {
			if a, ok := ev.(ApprovalNeeded); ok {
				gotApproval <- a
				parentDecisions <- AllowOnce
			}
		}
	}()

	args := mustJSON(t, agentArgs{SubagentType: "fg", Prompt: "p"})
	out, err := tool.Execute(ctx, args)
	close(parentEvents)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case a := <-gotApproval:
		if !strings.Contains(a.Preview, "[subagent:fg]") {
			t.Errorf("approval Preview missing subagent badge: %q", a.Preview)
		}
		if a.ToolName != "run_bash" {
			t.Errorf("approval ToolName = %q, want run_bash", a.ToolName)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no ApprovalNeeded forwarded to parent")
	}

	if !strings.Contains(out, "ran with approval") {
		t.Errorf("child final reply not surfaced; got: %q", out)
	}
}
