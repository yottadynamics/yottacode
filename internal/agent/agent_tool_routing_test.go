package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// countingStreamer wraps a scripted reply and counts ChatStream calls.
// Used by the cost-regression guard below.
type countingStreamer struct {
	mu    sync.Mutex
	calls int
}

func (c *countingStreamer) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	out := make(chan adapter.StreamEvent, 4)
	go func() {
		defer close(out)
		out <- sseDone("done")
	}()
	return out
}

// TestMainThreadDispatch_NoExtraRouterCall is the cost-regression guard
// the whole design rests on: the main-thread loop must issue exactly one
// model call per iteration. If a per-turn classifier or router call is
// ever inserted into loop.go's dispatch path, the count jumps and this
// fails — protecting the prompt-cache-locality property that makes
// routing a saving rather than a cost.
func TestMainThreadDispatch_NoExtraRouterCall(t *testing.T) {
	cs := &countingStreamer{}
	cfg := LoopConfig{
		Adapter:       cs,
		Registry:      NewRegistry(),
		MaxIterations: 3,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if cs.calls != 1 {
		t.Errorf("main-thread turn made %d model calls, want exactly 1 (no per-turn router/classifier overhead)", cs.calls)
	}
}

// exploreAgent mirrors the builtin Explore agent (read-only tool set).
// After the smart-default change it routes to the smart model in auto
// mode like every other delegated agent — read-only no longer means fast.
var exploreAgent = subagents.AgentConfig{
	Name:        "Explore",
	Description: "read-only search",
	Tools:       []string{"read_file", "grep", "glob", "list_dir"},
	Prompt:      "be terse",
	Source:      "test",
}

// TestRouteChildModel_AutoRoutesExploreToSmart locks the smart-default
// policy: Explore (and any read-only agent) routes to the smart model in
// auto mode, NOT the fast model. The fast model is summarization-only.
func TestRouteChildModel_AutoRoutesExploreToSmart(t *testing.T) {
	active := &scriptedStreamer{}
	smart := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{exploreAgent}, active, false)
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&exploreAgent)
	if got != Streamer(smart) {
		t.Errorf("Explore in auto mode should route to the smart model, not fast/active")
	}
	if model != "smart-1" {
		t.Errorf("model = %q, want smart-1", model)
	}
}

func TestRouteChildModel_GeneralRoutesToSmartModel(t *testing.T) {
	active := &scriptedStreamer{}
	smart := &scriptedStreamer{}
	wildcard := subagents.AgentConfig{Name: "general", Prompt: "x", Source: "test"} // no Tools = inherit all
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{wildcard}, active, false)
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&wildcard)
	if got != Streamer(smart) {
		t.Errorf("a delegated agent in auto mode should route to the configured smart model, not the active model")
	}
	if model != "smart-1" {
		t.Errorf("model = %q, want smart-1", model)
	}
}

func TestRouteChildModel_InheritsActiveWhenNoSmartConfigured(t *testing.T) {
	active := &scriptedStreamer{}
	wildcard := subagents.AgentConfig{Name: "general", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{wildcard}, active, false)
	// No SmartAdapter → inherit the active model.
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&wildcard)
	if got != Streamer(active) {
		t.Errorf("with no smart adapter, a delegated agent should inherit the active adapter")
	}
	if model != "" {
		t.Errorf("inherited model name should be empty, got %q", model)
	}
}

func TestRouteChildModel_ManualModeSkipsHeuristic(t *testing.T) {
	active := &scriptedStreamer{}
	smart := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{exploreAgent}, active, false)
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = false // manual mode: the smart heuristic does not apply

	got, model := tool.routeChildModel(&exploreAgent)
	if got != Streamer(active) {
		t.Errorf("manual mode must not auto-route; a non-explicit agent inherits the active model")
	}
	if model != "" {
		t.Errorf("model = %q, want empty (inherited)", model)
	}
}

// TestRouteChildModel_ExplicitModelWins also proves the fast model is
// still reachable for a subagent — but only via an explicit `model:`.
func TestRouteChildModel_ExplicitModelWins(t *testing.T) {
	smart := &scriptedStreamer{}
	explicitFast := &scriptedStreamer{}
	cfg := subagents.AgentConfig{Name: "custom", Model: "fast-1", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, smart, false)
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = true
	tool.ModelResolver = func(name string) Streamer {
		if name == "fast-1" {
			return explicitFast
		}
		return nil
	}

	got, model := tool.routeChildModel(&cfg)
	if got != Streamer(explicitFast) {
		t.Errorf("explicit frontmatter model should win over the smart heuristic")
	}
	if model != "fast-1" {
		t.Errorf("model = %q, want fast-1", model)
	}
}

func TestRouteChildModel_UnresolvedExplicitModelFallsBack(t *testing.T) {
	smart := &scriptedStreamer{}
	cfg := subagents.AgentConfig{Name: "custom", Model: "ghost", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, smart, false)
	tool.RouteAuto = true
	tool.ModelResolver = func(string) Streamer { return nil } // unknown name

	got, model := tool.routeChildModel(&cfg)
	if got != Streamer(smart) {
		t.Errorf("an unresolvable explicit model should degrade to the parent adapter")
	}
	if model != "" {
		t.Errorf("model = %q, want empty on fallback", model)
	}
}

func TestRouteChildModel_DisabledRoutingInherits(t *testing.T) {
	active := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{exploreAgent}, active, false)
	// RouteAuto false, no resolver — routing fully off.
	got, model := tool.routeChildModel(&exploreAgent)
	if got != Streamer(active) || model != "" {
		t.Errorf("with routing off the child must inherit the parent adapter, got model=%q", model)
	}
}

// TestAgentTool_SubagentForwardsFallback proves a subagent's model-chain
// fallover is forwarded to the parent (tagged with the agent type) so the
// TUI can surface it live, not just bury it in the child transcript.
func TestAgentTool_SubagentForwardsFallback(t *testing.T) {
	smart := &scriptedStreamer{turns: [][]adapter.StreamEvent{{
		{Kind: adapter.EventFallback, FallbackFrom: "anthropic/opus", FallbackTo: "openai/gpt-4o", FallbackReason: "timeout"},
		sseDone("done after fallover"),
	}}}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{exploreAgent}, &scriptedStreamer{}, true)
	tool.SmartAdapter = smart
	tool.SmartModel = "opus"
	tool.RouteAuto = true

	parentEvents := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), parentEvents)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "go"})
	done := make(chan string, 1)
	go func() {
		out, _ := tool.Execute(ctx, args)
		done <- out
		close(parentEvents)
	}()

	var got *Fallback
	for ev := range parentEvents {
		if f, ok := ev.(Fallback); ok {
			fb := f
			got = &fb
		}
	}
	<-done

	if got == nil {
		t.Fatal("expected a Fallback forwarded to the parent")
	}
	if got.Agent != "Explore" {
		t.Errorf("forwarded fallback Agent = %q, want Explore", got.Agent)
	}
	if got.To != "openai/gpt-4o" {
		t.Errorf("forwarded fallback To = %q, want openai/gpt-4o", got.To)
	}
}

// TestAgentTool_SubagentDoneCarriesRoutedModel is the end-to-end proof:
// an Explore subagent dispatched in auto mode actually runs on the smart
// adapter (its scripted reply is returned) and the SubagentDone + Task
// record carry the smart model name for /usage + /subagents.
func TestAgentTool_SubagentDoneCarriesRoutedModel(t *testing.T) {
	smart := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("smart model handled it")},
	}}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{exploreAgent}, &scriptedStreamer{}, true)
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = true

	parentEvents := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), parentEvents)

	args := mustJSON(t, agentArgs{SubagentType: "Explore", Prompt: "find it"})
	done := make(chan string, 1)
	go func() {
		out, _ := tool.Execute(ctx, args)
		done <- out
		close(parentEvents)
	}()

	var doneModel string
	for ev := range parentEvents {
		if d, ok := ev.(SubagentDone); ok {
			doneModel = d.Model
		}
	}
	out := <-done

	if doneModel != "smart-1" {
		t.Errorf("SubagentDone.Model = %q, want smart-1", doneModel)
	}
	if out != "smart model handled it" {
		t.Errorf("result = %q, want the smart adapter's reply (proves it actually dispatched there)", out)
	}
	tasks := tool.Tasks.List()
	if len(tasks) != 1 || tasks[0].Model != "smart-1" {
		t.Errorf("Task.Model not recorded; tasks=%+v", tasks)
	}
}
