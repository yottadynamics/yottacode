package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/checkpoint"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// scrollbackText is everything the model has printed so far, ANSI removed.
func scrollbackText(m Model) string { return stripANSI(strings.Join(m.historyLines, "\n")) }

// history snapshots the conversation under the lock the agent loop uses.
func history(m Model) []adapter.Message {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	return append([]adapter.Message(nil), m.sess.Messages...)
}

const drResult = "Answer. [S1]\n\nCost: 6 agent runs (…)\n\nFull report (sources and coverage notes): /tmp/r.md\n"

func drWake(id string) agent.SubagentBackgroundDone {
	return agent.SubagentBackgroundDone{TaskID: id, AgentType: agent.DeepResearchTaskType, Result: drResult, Duration: time.Minute}
}

// Stage 1: a finished run's summary is printed and recorded by the TUI itself.
// No model turn, so no relay cost and nothing the model could add to it.
func TestDeliverDeepResearch_PrintsRecordsAndStartsNoTurn(t *testing.T) {
	m := newTestModel(t)
	before := len(history(m))

	m.deliverDeepResearch(drWake("0123456789ab"))

	out := scrollbackText(m)
	for _, want := range []string{"Answer. [S1]", "Cost: 6 agent runs", "/tmp/r.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("scrollback missing %q:\n%s", want, out)
		}
	}
	h := history(m)
	if len(h) != before+2 {
		t.Fatalf("history grew by %d, want 2", len(h)-before)
	}
	user, asst := h[before], h[before+1]
	if user.Role != adapter.RoleUser || !strings.Contains(user.Content, "deep-research 01234567") {
		t.Errorf("user note wrong: %+v", user)
	}
	if asst.Role != adapter.RoleAssistant || asst.Content != drResult {
		t.Errorf("assistant message must be the summary verbatim, got %q", asst.Content)
	}
	if m.turnActive {
		t.Error("delivering a result must not start a turn")
	}
}

// An empty assistant message in history is rejected by providers and would
// break every later turn, so an empty result is replaced, never recorded.
func TestDeliverDeepResearch_NeverRecordsAnEmptyAssistantMessage(t *testing.T) {
	m := newTestModel(t)
	before := len(history(m))
	m.deliverDeepResearch(agent.SubagentBackgroundDone{TaskID: "0123456789ab", AgentType: agent.DeepResearchTaskType, Result: "  \n"})
	h := history(m)
	if len(h) != before+2 {
		t.Fatalf("history grew by %d, want 2", len(h)-before)
	}
	if strings.TrimSpace(h[len(h)-1].Content) == "" {
		t.Error("the recorded assistant message is empty")
	}
}

// The drain path (every wake call site goes through startSubagentWakeTurn)
// delivers deep-research completions without starting a turn.
func TestStartSubagentWakeTurn_DeepResearchStartsNoTurn(t *testing.T) {
	m := newTestModel(t)
	m.pendingSubagentWakes = []agent.SubagentBackgroundDone{drWake("0123456789ab")}
	before := len(history(m))

	next, cmd := m.startSubagentWakeTurn()
	got := next.(Model)
	if cmd != nil || got.turnActive {
		t.Errorf("no model turn expected, got cmd=%v turnActive=%v", cmd != nil, got.turnActive)
	}
	if len(got.pendingSubagentWakes) != 0 {
		t.Errorf("the wake must be consumed, %d left", len(got.pendingSubagentWakes))
	}
	if h := history(got); len(h) != before+2 || h[len(h)-1].Content != drResult {
		t.Errorf("summary not recorded in history: %d messages", len(h))
	}
}

// A batch that also holds another subagent still wakes the model for THAT one;
// the deep-research result is delivered natively and never reaches the model
// as raw material to editorialize on.
func TestStartSubagentWakeTurn_MixedBatchDeliversResearchAndWakesForTheRest(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.pendingSubagentWakes = []agent.SubagentBackgroundDone{
		drWake("0123456789ab"),
		{TaskID: "ffffffffffff", AgentType: "Explore", Result: "found it in parser.go"},
	}

	next, cmd := m.startSubagentWakeTurn()
	got := next.(Model)
	defer func() {
		if got.turnCancel != nil {
			got.turnCancel()
		}
	}()
	if cmd == nil || !got.turnActive {
		t.Fatal("the Explore completion should still wake the model")
	}
	var wakeMsg string
	var nativeSummary bool
	for _, msg := range history(got) {
		if msg.Role == adapter.RoleUser && strings.Contains(msg.Content, "found it in parser.go") {
			wakeMsg = msg.Content
		}
		if msg.Role == adapter.RoleAssistant && msg.Content == drResult {
			nativeSummary = true
		}
	}
	if wakeMsg == "" {
		t.Fatal("the model's wake message for the other subagent is missing")
	}
	if strings.Contains(wakeMsg, "Answer. [S1]") {
		t.Errorf("the wake message must not carry the deep-research result:\n%s", wakeMsg)
	}
	if !nativeSummary {
		t.Error("the deep-research summary should have been delivered natively")
	}
}

func deepResearchTestModel(t *testing.T, allowBackground bool) (Model, *agent.DeepResearchTool, *subagents.Registry, chan agent.SubagentBackgroundDone) {
	t.Helper()
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	reg := subagents.NewRegistry()
	m.subagentTasks = reg
	done := make(chan agent.SubagentBackgroundDone, 4)
	at := &agent.AgentTool{AllowBackground: allowBackground, Tasks: reg, TranscriptDir: t.TempDir()}
	at.SetBackgroundDoneCallback(func(ev agent.SubagentBackgroundDone) { done <- ev })
	tool := &agent.DeepResearchTool{Agent: at, Cwd: agent.NewCwdRef(t.TempDir())}
	m.cfg.Registry.Register(tool)
	return m, tool, reg, done
}

// Stage 2: the slash command starts the run itself — no model turn at the
// start either — and records the exchange so follow-ups have context.
func TestCmdDeepResearch_StartsDirectlyWithoutAModelTurn(t *testing.T) {
	m, _, reg, done := deepResearchTestModel(t, true)

	got, cmd := cmdDeepResearch(m, []string{"--breadth", "2", "compare", "x", "and", "y"})
	if cmd != nil || got.turnActive {
		t.Fatalf("no model turn expected, got cmd=%v turnActive=%v", cmd != nil, got.turnActive)
	}
	// With no real subagents configured the detached run fails every agent and
	// can finish within a millisecond, so don't assert it is still running —
	// only that it was registered, as a background task, with the right query.
	tasks := reg.List()
	if len(tasks) != 1 || tasks[0].AgentType != agent.DeepResearchTaskType || !tasks[0].Background || !tasks[0].NotifyOnDone {
		t.Fatalf("expected one background deep-research task, got %+v", tasks)
	}
	if tasks[0].Prompt != "compare x and y" {
		t.Errorf("task query = %q (the --breadth flag must not leak into it)", tasks[0].Prompt)
	}
	out := scrollbackText(got)
	if !strings.Contains(out, "/deep-research --breadth 2 compare x and y") || !strings.Contains(out, "started in the background") {
		t.Errorf("scrollback should show the command and the started line:\n%s", out)
	}
	h := history(got)
	if n := len(h); n < 2 || h[n-2].Role != adapter.RoleUser || h[n-2].Content != "/deep-research --breadth 2 compare x and y" ||
		h[n-1].Role != adapter.RoleAssistant || !strings.Contains(h[n-1].Content, "started in the background") {
		t.Errorf("the start must be recorded as a user/assistant pair, tail = %+v", h[max(0, n-2):])
	}

	// Let the detached run end before the test's temp dirs are removed.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never finished")
	}
}

// Context summarization owns the history and the session save until it lands,
// so the direct start (which appends to both) must wait for it.
func TestCmdDeepResearch_RefusesWhileSummarizing(t *testing.T) {
	m, _, reg, _ := deepResearchTestModel(t, true)
	m.summarizing = true
	before := len(history(m))

	got, cmd := cmdDeepResearch(m, []string{"q"})
	if cmd != nil || got.turnActive {
		t.Error("nothing should start while summarizing")
	}
	if !strings.Contains(scrollbackText(got), "summarization is running") {
		t.Errorf("the user should be told why:\n%s", scrollbackText(got))
	}
	if len(reg.List()) != 0 || len(history(got)) != before {
		t.Error("a refused start must not register a task or touch history")
	}
}

// The direct start leaves a /checkpoints entry, like a normal turn: labelled
// with the command, holding the conversation as it stood BEFORE the exchange
// was appended, so restoring it rewinds the start.
func TestCmdDeepResearch_CreatesACheckpoint(t *testing.T) {
	m, _, _, done := deepResearchTestModel(t, true)
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Checkpoints = store
	before := len(history(m))

	got, _ := cmdDeepResearch(m, []string{"compare", "x", "and", "y"})
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	man, err := store.LoadManifest(m.sess.ID)
	if err != nil || len(man.Entries) != 1 {
		t.Fatalf("want exactly one checkpoint, got %d (err %v)", len(man.Entries), err)
	}
	if !strings.Contains(man.Entries[0].PromptPreview, "/deep-research compare x and y") {
		t.Errorf("checkpoint label = %q", man.Entries[0].PromptPreview)
	}
	pre, err := store.LoadMessages(m.sess.ID, man.Entries[0].CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != before {
		t.Errorf("snapshot holds %d messages, want the %d from before the command", len(pre), before)
	}
	if len(history(got)) != before+2 {
		t.Errorf("live history should have the exchange appended, got %d", len(history(got))-before)
	}
}

// A refused start must not leave a checkpoint for a command that never ran.
func TestCmdDeepResearch_RefusedStartLeavesNoCheckpoint(t *testing.T) {
	m, tool, reg, _ := deepResearchTestModel(t, true)
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Checkpoints = store
	tool.Agent.MaxConcurrentSubagents = 1
	reg.Add(&subagents.Task{ID: "busy", AgentType: "x", Status: subagents.TaskRunning, Background: true})

	cmdDeepResearch(m, []string{"q"})

	if man, _ := store.LoadManifest(m.sess.ID); len(man.Entries) != 0 {
		t.Errorf("a refused start left %d checkpoint(s)", len(man.Entries))
	}
}

// Cases where the model-driven path must still be used.
func TestCmdDeepResearch_FallsBackToTheModelPath(t *testing.T) {
	cases := map[string]func(m *Model) *agent.DeepResearchTool{
		"plan mode keeps the tool blocked": func(m *Model) *agent.DeepResearchTool {
			ps := &agent.PlanModeState{}
			ps.Active.Store(true)
			m.cfg.PlanMode = ps
			return nil
		},
		"session cannot background": func(m *Model) *agent.DeepResearchTool { return nil },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			m, _, _, _ := deepResearchTestModel(t, name != "session cannot background")
			setup(&m)
			got, cmd := cmdDeepResearch(m, []string{"q"})
			defer func() {
				if got.turnCancel != nil {
					got.turnCancel()
				}
			}()
			if cmd == nil || !got.turnActive {
				t.Fatalf("expected the model-driven turn, got cmd=%v turnActive=%v", cmd != nil, got.turnActive)
			}
			if !strings.Contains(history(got)[len(history(got))-1].Content, "deep_research") {
				t.Errorf("fallback should submit the directive that calls the tool")
			}
		})
	}
	t.Run("tool not registered", func(t *testing.T) {
		m := newTestModel(t)
		m.cfg.Adapter = stubAdapterNoStream{}
		got, cmd := cmdDeepResearch(m, []string{"q"})
		defer func() {
			if got.turnCancel != nil {
				got.turnCancel()
			}
		}()
		if cmd == nil || !got.turnActive {
			t.Errorf("expected the model-driven turn, got cmd=%v turnActive=%v", cmd != nil, got.turnActive)
		}
	})
}

func TestCmdDeepResearch_BadArgsStartNothing(t *testing.T) {
	m, _, reg, _ := deepResearchTestModel(t, true)
	before := len(history(m))
	for _, args := range [][]string{{"--breadth", "9", "q"}, {"--breadth"}, nil} {
		got, cmd := cmdDeepResearch(m, args)
		if cmd != nil || got.turnActive || len(reg.List()) != 0 || len(history(got)) != before {
			t.Errorf("args %v must start nothing", args)
		}
		if !strings.Contains(scrollbackText(got), "[deep-research]") {
			t.Errorf("args %v should print a usage error", args)
		}
	}
}

// A refused start (concurrency cap) is shown to the user — not retried through
// the blocking model path — and leaves no phantom history.
func TestCmdDeepResearch_AdmissionErrorIsShown(t *testing.T) {
	m, tool, reg, _ := deepResearchTestModel(t, true)
	tool.Agent.MaxConcurrentSubagents = 1
	reg.Add(&subagents.Task{ID: "busy", AgentType: "x", Status: subagents.TaskRunning, Background: true})
	before := len(history(m))

	got, cmd := cmdDeepResearch(m, []string{"q"})
	if cmd != nil || got.turnActive {
		t.Error("a refused start must not fall back to a model turn")
	}
	if !strings.Contains(scrollbackText(got), "at most 1 background") {
		t.Errorf("the cap error should be shown:\n%s", scrollbackText(got))
	}
	if len(history(got)) != before || len(reg.List()) != 1 {
		t.Error("a refused start must leave no history entry or task")
	}
}

// Other subagents keep the generic wake wording.
func TestWakeMessage_OrdinarySubagentUnchanged(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{{TaskID: "cccccccccccc", AgentType: "Explore", Result: "found it"}})
	if !strings.Contains(msg, "continue the work they were part of") {
		t.Errorf("ordinary background subagents must keep the generic closing:\n%s", msg)
	}
}
