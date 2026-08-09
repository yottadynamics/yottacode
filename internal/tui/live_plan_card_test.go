package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// samplePlan is the in-flight plan most tests below operate on:
// one done, one in-progress, one pending. Lets the assertions look
// at all three status icons + the (2 items not yet done) count math.
func samplePlan() []agent.Todo {
	return []agent.Todo{
		{Content: "design", Status: agent.TodoCompleted},
		{Content: "review", Status: agent.TodoInProgress},
		{Content: "ship", Status: agent.TodoPending},
	}
}

func TestRenderTodoCardFromTodos_HeaderBodyFooter(t *testing.T) {
	got := stripANSI(renderTodoCardFromTodos(samplePlan(), 80))
	for _, want := range []string{
		"┌ Plan: 3 items (1 done)",
		"✓ design",
		"▸ review",
		"· ship",
		"└ plan updated: 3 items (1 done)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered card missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderTodoCardFromTodos_EmptyShowsClearedFooter(t *testing.T) {
	got := stripANSI(renderTodoCardFromTodos(nil, 80))
	for _, want := range []string{
		"┌ Plan: 0 items (0 done)",
		"(empty plan)",
		"└ plan cleared",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("empty-plan card missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderTodoCardFromTodos_WrapsLongRowsUnderStatusGutter(t *testing.T) {
	got := stripANSI(renderTodoCardFromTodos([]agent.Todo{{
		Content: "review a very long plan item that wraps cleanly on narrow terminals without losing the status gutter",
		Status:  agent.TodoInProgress,
	}}, 48))
	rows := strings.Split(got, "\n")
	if len(rows) < 4 {
		t.Fatalf("expected wrapped todo card rows, got:\n%s", got)
	}
	if !strings.Contains(got, "│ ▸ review") {
		t.Fatalf("first todo row should keep status icon:\n%s", got)
	}
	if !strings.Contains(got, "│   cleanly") {
		t.Fatalf("wrapped todo continuation should align under todo text:\n%s", got)
	}
	for i, row := range rows {
		if len([]rune(row)) > 48 {
			t.Fatalf("todo card row %d exceeded width 48: %q\n%s", i, row, got)
		}
	}
}

func TestModel_RenderLivePlanCard_EmptyReturnsBlank(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderLivePlanCard(); got != "" {
		t.Errorf("renderLivePlanCard with no plan should return \"\"; got %q", got)
	}
}

func TestModel_RenderLivePlanCard_PopulatedRendersBody(t *testing.T) {
	m := newTestModel(t)
	m.livePlan = samplePlan()
	got := stripANSI(m.renderLivePlanCard())
	if !strings.Contains(got, "Plan: 3 items (1 done)") {
		t.Errorf("live card should carry the synthesized header; got %q", got)
	}
	if !strings.Contains(got, "▸ review") {
		t.Errorf("live card should carry per-item rows; got %q", got)
	}
}

func TestModel_TodoUpdate_SetsLivePlanArmsSnapshotAndSyncsSession(t *testing.T) {
	m := newTestModel(t)
	plan := samplePlan()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: plan}})

	if len(m.livePlan) != len(plan) {
		t.Errorf("livePlan not set on TodoUpdate: got %d items, want %d", len(m.livePlan), len(plan))
	}
	if !m.livePlanTouched {
		t.Errorf("livePlanTouched should arm after TodoUpdate; got false")
	}
	if m.sess == nil || len(m.sess.Todos) != len(plan) {
		t.Errorf("session.Todos not synced from TodoUpdate")
	}
}

func TestModel_TodoUpdate_EmptyClearsLivePlan(t *testing.T) {
	m := newTestModel(t)
	m.livePlan = samplePlan()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: nil}})

	if len(m.livePlan) != 0 {
		t.Errorf("empty TodoUpdate should clear livePlan; got %d items", len(m.livePlan))
	}
	if got := m.renderLivePlanCard(); got != "" {
		t.Errorf("cleared plan should produce no live card; got %q", got)
	}
}

func TestModel_ToolResultForTodoWrite_DoesNotEmitScrollbackCard(t *testing.T) {
	m := newTestModel(t)
	// Buffer the start info the way the real flow does, then deliver
	// a ToolResult for todo_write. The suppression branch should clear
	// pending state without calling appendLine, so transcript stays
	// empty.
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "todo_write", Preview: "Plan: 1 items (0 done)"}})
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{
		ToolName: "todo_write",
		Output:   "plan updated: 1 items (0 done)",
	}})

	if got := m.transcript.String(); got != "" {
		t.Errorf("ToolResult for todo_write should not emit a scrollback card; transcript = %q", got)
	}
	if m.pendingToolName != "" || m.pendingToolPreview != "" || m.pendingToolArgs != "" || !m.pendingToolStart.IsZero() {
		t.Errorf("ToolResult should still clear pendingTool* state; got name=%q preview=%q args=%q start=%v",
			m.pendingToolName, m.pendingToolPreview, m.pendingToolArgs, m.pendingToolStart)
	}
}

func TestModel_SuppressedToolResultClearsPendingStart(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
	}{
		{name: "agent", toolName: agent.AgentToolName},
		{name: "todo_write", toolName: "todo_write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			// A suppressed result must clear the timestamp too; otherwise a
			// later result without a fresh start could inherit this stale time
			// and render a bogus slow-call duration tag on the wrong card.
			m.pendingToolName = tc.toolName
			m.pendingToolPreview = tc.toolName + " preview"
			m.pendingToolArgs = `{}`
			m.pendingToolStart = time.Now().Add(-2 * time.Minute)

			m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: tc.toolName}})

			if !m.pendingToolStart.IsZero() {
				t.Fatalf("suppressed %s result left stale pendingToolStart: %v", tc.toolName, m.pendingToolStart)
			}
		})
	}
}

func TestModel_StaleSuppressedToolStartDoesNotTagNextResult(t *testing.T) {
	m := newTestModel(t)
	// Simulate the risky interleaving from parallel tool execution: a
	// suppressed Agent result clears the pending buffer, then an ordinary
	// ToolResult arrives without a matching fresh ToolStart. The fallback
	// card should render without inheriting Agent's old duration.
	m.pendingToolName = agent.AgentToolName
	m.pendingToolPreview = "Agent: review"
	m.pendingToolArgs = `{}`
	m.pendingToolStart = time.Now().Add(-2 * time.Minute)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: agent.AgentToolName}})

	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{
		ToolName: "read_file",
		Output:   "hello",
	}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	got := stripANSI(m.transcript.String())
	if strings.Contains(got, "2m") {
		t.Fatalf("ordinary card inherited stale suppressed-tool duration:\n%s", got)
	}
	if !strings.Contains(got, "Read(read_file)") && !strings.Contains(got, "read_file") {
		t.Fatalf("ordinary card should still render after grouped flush; got:\n%s", got)
	}
	if !strings.Contains(got, "1 line · 5 bytes") {
		t.Fatalf("ordinary card should still show read footer after grouped flush; got:\n%s", got)
	}
}

func TestModel_ConsecutiveReadResultsGroupIntoOneCard(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(a.go)", ArgsJSON: `{"path":"a.go"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "read_file", Output: "one\n"}})
	if got := m.transcript.String(); got != "" {
		t.Fatalf("groupable read should buffer, not render immediately: %q", got)
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(b.go)", ArgsJSON: `{"path":"b.go"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "read_file", Output: "two\nthree\n"}})
	if got := m.transcript.String(); got != "" {
		t.Fatalf("second groupable read should still buffer until a flush boundary: %q", got)
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "┌ Read · 2 calls") {
		t.Fatalf("expected grouped read card, got:\n%s", got)
	}
	if strings.Count(got, "Read(") < 2 {
		t.Fatalf("expected two grouped read rows, got:\n%s", got)
	}
}

func TestModel_ConsecutiveListResultsGroupIntoOneCard(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "list_dir", Preview: "list_dir(.)", ArgsJSON: `{"path":"."}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "list_dir", Output: "d\tbin\nf\tREADME.md\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "list_project_structure", Preview: "list_project_structure(.)", ArgsJSON: `{"path":".","max_depth":2}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "list_project_structure", Output: "d\t4096\t2026-05-03T02:09:50Z\tinternal\nf\t91\t2026-05-03T02:09:50Z\tgo.mod\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "┌ List · 2 calls") {
		t.Fatalf("expected grouped list card, got:\n%s", got)
	}
	if !strings.Contains(got, "List(.) — 2 entries") || !strings.Contains(got, "Tree(., depth=2) — 2 entries") {
		t.Fatalf("expected grouped list rows, got:\n%s", got)
	}
}

func TestModel_ConsecutiveGlobResultsGroupIntoOneCard(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "glob", Preview: "glob(*.go)", ArgsJSON: `{"pattern":"*.go"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "glob", Output: "main.go\ninternal/tui/model.go\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "glob", Preview: "glob(*.md)", ArgsJSON: `{"pattern":"*.md"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "glob", Output: "README.md\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "┌ Glob · 2 calls") {
		t.Fatalf("expected grouped glob card, got:\n%s", got)
	}
	if !strings.Contains(got, "Glob(*.go) — 2 matches") || !strings.Contains(got, "Glob(*.md) — 1 match") {
		t.Fatalf("expected grouped glob rows, got:\n%s", got)
	}
}

func TestModel_NonGroupableToolFlushesPendingReadGroup(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(a.go)", ArgsJSON: `{"path":"a.go"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "read_file", Output: "one\n"}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "grep", Preview: `grep("x" in .)`, ArgsJSON: `{"pattern":"x","path":"."}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "grep", Output: "./a.go:1:x\n"}})
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "┌ Read · 1 call") {
		t.Fatalf("pending grouped reads should flush before non-groupable card:\n%s", got)
	}
	if !strings.Contains(got, "┌ Grep(\"x\")") {
		t.Fatalf("non-groupable tool should still render its own card:\n%s", got)
	}
}

func TestModel_ErroredReadDoesNotGroup(t *testing.T) {
	m := newTestModel(t)
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolStart{ToolName: "read_file", Preview: "read_file(a.go)", ArgsJSON: `{"path":"a.go"}`}})
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ToolResult{ToolName: "read_file", Output: "boom", Errored: true}})
	got := stripANSI(m.transcript.String())
	if strings.Contains(got, "Read · ") {
		t.Fatalf("errored read should not render grouped card:\n%s", got)
	}
	// Glyph-agnostic: a standalone card's opening corner is ┌ when
	// clean or ╔ when errored (see gutterFor) — this test only cares
	// that it's the standalone shape, not which outcome glyph it uses.
	if !strings.Contains(got, "Read(a.go)") && !strings.Contains(got, "read_file(a.go)") {
		t.Fatalf("errored read should render standalone card:\n%s", got)
	}
}

func TestModel_TurnDone_CommitsSnapshotAndClearsLiveCard(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: samplePlan()}})
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "Plan: 3 items (1 done)") {
		t.Errorf("TurnDone should commit a plan snapshot to scrollback when touched; transcript = %q", got)
	}
	if m.livePlanTouched {
		t.Errorf("TurnDone should reset livePlanTouched; still true")
	}
	// Live card must clear after the snapshot lands — otherwise the
	// user sees the plan twice (once in scrollback, once still in the
	// live frame) for the entire inter-turn idle period.
	if len(m.livePlan) != 0 {
		t.Errorf("TurnDone must clear livePlan after committing the snapshot; got %d items", len(m.livePlan))
	}
	if got := m.renderLivePlanCard(); got != "" {
		t.Errorf("live card should be empty after TurnDone commit; got %q", got)
	}
}

func TestModel_TurnDone_PreservesLivePlanWhenUntouched(t *testing.T) {
	// A turn that doesn't touch the plan (e.g. resume → first turn of
	// pure conversation) must leave the seeded live card alone so it
	// stays visible between turns. Only a turn that COMMITTED a
	// snapshot clears the live card.
	m := newTestModel(t)
	m.livePlan = samplePlan()
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	if len(m.livePlan) != 3 {
		t.Errorf("untouched TurnDone must preserve livePlan; got %d items", len(m.livePlan))
	}
}

func TestModel_TurnDone_SkipsSnapshotWhenPlanUntouched(t *testing.T) {
	m := newTestModel(t)
	// Pre-seed a plan WITHOUT firing TodoUpdate, so livePlanTouched
	// stays false (this is the resume-style state).
	m.livePlan = samplePlan()
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	got := stripANSI(m.transcript.String())
	if strings.Contains(got, "Plan: 3 items") {
		t.Errorf("TurnDone should NOT commit a snapshot when the plan was not touched; transcript = %q", got)
	}
}

func TestModel_TurnDone_SkipsSnapshotWhenPlanClearedThisTurn(t *testing.T) {
	m := newTestModel(t)
	m.livePlan = samplePlan()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: nil}}) // clears + arms
	m.transcript.Reset()
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnDone{}})

	got := stripANSI(m.transcript.String())
	if strings.Contains(got, "(empty plan)") || strings.Contains(got, "plan cleared") {
		t.Errorf("TurnDone with len(livePlan)==0 should not emit any plan artifact; transcript = %q", got)
	}
}

func TestModel_TurnInterrupted_ResetsTouchedFlag(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: samplePlan()}})
	if !m.livePlanTouched {
		t.Fatalf("setup: livePlanTouched should be true before interrupt")
	}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TurnInterrupted{}})
	if m.livePlanTouched {
		t.Errorf("TurnInterrupted should reset livePlanTouched; still true")
	}
}

func TestModel_View_RendersLivePlanCardWhenPopulated(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.TodoUpdate{Todos: samplePlan()}})
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Plan: 3 items (1 done)") {
		t.Errorf("View() should show the live plan card; got:\n%s", view)
	}
}

// Compile-time guard that we're using tea.Msg variants correctly in tests.
var _ tea.Msg = agentEventMsg{}
