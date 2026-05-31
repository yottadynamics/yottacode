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

// readOnlyExplore mirrors the builtin Explore agent's read-only tool set.
var readOnlyExplore = subagents.AgentConfig{
	Name:        "Explore",
	Description: "read-only search",
	Tools:       []string{"read_file", "grep", "glob", "list_dir"},
	Prompt:      "be terse",
	Source:      "test",
}

func TestAgentIsReadOnly(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		want  bool
	}{
		{"explore-readonly", []string{"read_file", "grep", "glob"}, true},
		{"plan-readonly", []string{"read_file", "grep", "todo_write"}, true},
		{"has-run-bash", []string{"read_file", "grep", "run_bash"}, false},
		{"has-write", []string{"read_file", "write_file"}, false},
		{"wildcard-inherit-all", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := subagents.AgentConfig{Name: tc.name, Tools: tc.tools}
			if got := agentIsReadOnly(&cfg); got != tc.want {
				t.Errorf("agentIsReadOnly(%v) = %v, want %v", tc.tools, got, tc.want)
			}
		})
	}
}

func TestRouteChildModel_AutoRoutesReadOnlyToFast(t *testing.T) {
	smart := &scriptedStreamer{}
	fast := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{readOnlyExplore}, smart, false)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&readOnlyExplore)
	if got != Streamer(fast) {
		t.Errorf("read-only agent in auto mode should route to FastAdapter")
	}
	if model != "fast-1" {
		t.Errorf("model = %q, want fast-1", model)
	}
}

func TestRouteChildModel_FullToolsetRoutesToSmartModel(t *testing.T) {
	active := &scriptedStreamer{}
	fast := &scriptedStreamer{}
	smart := &scriptedStreamer{}
	wildcard := subagents.AgentConfig{Name: "general", Prompt: "x", Source: "test"} // no Tools = inherit all
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{wildcard}, active, false)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&wildcard)
	if got != Streamer(smart) {
		t.Errorf("non-read-only agent in auto mode should route to the configured smart model, not the active model")
	}
	if model != "smart-1" {
		t.Errorf("model = %q, want smart-1", model)
	}
}

func TestRouteChildModel_FullToolsetInheritsActiveWhenNoSmartConfigured(t *testing.T) {
	active := &scriptedStreamer{}
	fast := &scriptedStreamer{}
	wildcard := subagents.AgentConfig{Name: "general", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{wildcard}, active, false)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
	// No SmartAdapter (e.g. only fast configured) → inherit active model.
	tool.RouteAuto = true

	got, model := tool.routeChildModel(&wildcard)
	if got != Streamer(active) {
		t.Errorf("with no smart adapter, a non-read-only agent should inherit the active adapter")
	}
	if model != "" {
		t.Errorf("inherited model name should be empty, got %q", model)
	}
}

func TestRouteChildModel_ManualModeSkipsHeuristic(t *testing.T) {
	active := &scriptedStreamer{}
	fast := &scriptedStreamer{}
	smart := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{readOnlyExplore}, active, false)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
	tool.SmartAdapter = smart
	tool.SmartModel = "smart-1"
	tool.RouteAuto = false // manual mode: neither fast nor smart heuristic applies

	got, model := tool.routeChildModel(&readOnlyExplore)
	if got != Streamer(active) {
		t.Errorf("manual mode must not auto-route; a non-explicit agent inherits the active model")
	}
	if model != "" {
		t.Errorf("model = %q, want empty (inherited)", model)
	}
}

func TestRouteChildModel_ExplicitModelWins(t *testing.T) {
	smart := &scriptedStreamer{}
	fast := &scriptedStreamer{}
	explicit := &scriptedStreamer{}
	cfg := subagents.AgentConfig{Name: "custom", Model: "my-model", Prompt: "x", Source: "test"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, smart, false)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
	tool.RouteAuto = true
	tool.ModelResolver = func(name string) Streamer {
		if name == "my-model" {
			return explicit
		}
		return nil
	}

	got, model := tool.routeChildModel(&cfg)
	if got != Streamer(explicit) {
		t.Errorf("explicit frontmatter model should win over the heuristic")
	}
	if model != "my-model" {
		t.Errorf("model = %q, want my-model", model)
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
	smart := &scriptedStreamer{}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{readOnlyExplore}, smart, false)
	// No FastAdapter, RouteAuto false, no resolver — routing fully off.
	got, model := tool.routeChildModel(&readOnlyExplore)
	if got != Streamer(smart) || model != "" {
		t.Errorf("with routing off the child must inherit the parent adapter, got model=%q", model)
	}
}

// TestAgentTool_SubagentDoneCarriesRoutedModel is the end-to-end proof:
// a read-only Explore subagent dispatched in auto mode actually runs on
// the fast adapter (its scripted reply is returned) and the SubagentDone
// + Task record carry the fast model name for /usage + /subagents.
func TestAgentTool_SubagentDoneCarriesRoutedModel(t *testing.T) {
	fast := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("fast model handled it")},
	}}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{readOnlyExplore}, &scriptedStreamer{}, true)
	tool.FastAdapter = fast
	tool.FastModel = "fast-1"
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

	if doneModel != "fast-1" {
		t.Errorf("SubagentDone.Model = %q, want fast-1", doneModel)
	}
	if out != "fast model handled it" {
		t.Errorf("result = %q, want the fast adapter's reply (proves it actually dispatched there)", out)
	}
	tasks := tool.Tasks.List()
	if len(tasks) != 1 || tasks[0].Model != "fast-1" {
		t.Errorf("Task.Model not recorded; tasks=%+v", tasks)
	}
}
