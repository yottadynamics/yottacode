package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

	got := stripANSI(m.transcript.String())
	if strings.Contains(got, "2m") {
		t.Fatalf("ordinary card inherited stale suppressed-tool duration:\n%s", got)
	}
	if !strings.Contains(got, "┌ read_file") || !strings.Contains(got, "└ 1 line · 5 bytes") {
		t.Fatalf("ordinary card should still render from fallback state; got:\n%s", got)
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
	view := stripANSI(m.View())
	if !strings.Contains(view, "Plan: 3 items (1 done)") {
		t.Errorf("View() should show the live plan card; got:\n%s", view)
	}
}

// Compile-time guard that we're using tea.Msg variants correctly in tests.
var _ tea.Msg = agentEventMsg{}
