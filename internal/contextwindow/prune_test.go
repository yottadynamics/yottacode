package contextwindow

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func call(id, name, args string) adapter.Message {
	return adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: name, ArgsJSON: args}}}
}

func result(id string, tokens int) adapter.Message {
	return adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", tokens*4)}
}

func TestPruneToolOutputs_ProtectsNewestAndBlanksStale(t *testing.T) {
	msgs := []adapter.Message{{Role: adapter.RoleUser, Content: "task"}}
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, call(id, "run_bash", `{}`), result(id, 10_000))
	}
	out, saved, ok := PruneToolOutputs(msgs)
	if !ok || saved < PruneMinSavingsTokens {
		t.Fatalf("expected prune, ok=%v saved=%d", ok, saved)
	}
	if len(out) != len(msgs) {
		t.Fatalf("message count changed")
	}
	// Newest results (within the 40k protected volume) are intact; oldest blanked.
	if !IsPrunedPlaceholder(out[2].Content) {
		t.Errorf("oldest result not pruned: %.40q", out[2].Content)
	}
	if IsPrunedPlaceholder(out[len(out)-1].Content) {
		t.Errorf("newest result must be protected")
	}
	// Pairing untouched and input not mutated.
	if out[1].ToolCalls[0].ID != "a" || out[2].ToolCallID != "a" {
		t.Errorf("pairing changed")
	}
	if IsPrunedPlaceholder(msgs[2].Content) {
		t.Errorf("input was mutated")
	}
}

func TestPruneToolOutputs_BelowMinSavingsNoChange(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		call("a", "run_bash", `{}`), result("a", 5_000),
		call("b", "run_bash", `{}`), result("b", 5_000),
	}
	if _, _, ok := PruneToolOutputs(msgs); ok {
		t.Fatal("small history must not be pruned")
	}
}

func TestPruneToolOutputs_SupersededReadPrunedEvenWhenProtected(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		call("a", "read_file", `{"path":"x.go"}`), result("a", 21_000),
		call("b", "read_file", `{"path":"x.go"}`), result("b", 3_000),
	}
	out, _, ok := PruneToolOutputs(msgs)
	if !ok {
		t.Fatal("expected superseded read to be pruned")
	}
	if !IsPrunedPlaceholder(out[2].Content) || !strings.Contains(out[2].Content, "read_file") {
		t.Errorf("stale read not pruned with tool name: %.60q", out[2].Content)
	}
	if IsPrunedPlaceholder(out[4].Content) {
		t.Errorf("latest read must stay")
	}
}

func TestPruneToolOutputs_Idempotent(t *testing.T) {
	msgs := []adapter.Message{{Role: adapter.RoleUser, Content: "task"}}
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, call(id, "run_bash", `{}`), result(id, 10_000))
	}
	once, _, _ := PruneToolOutputs(msgs)
	if _, _, ok := PruneToolOutputs(once); ok {
		t.Fatal("second prune should find nothing to do")
	}
}

func TestPruneToolOutputs_PartialReadsNotSuperseded(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		call("a", "read_file", `{"path":"x.go","offset":1,"limit":200}`), result("a", 21_000),
		call("b", "read_file", `{"path":"x.go","offset":900,"limit":200}`), result("b", 3_000),
	}
	if out, _, ok := PruneToolOutputs(msgs); ok {
		t.Fatalf("a read of different lines must not be treated as superseded: %.50q", out[2].Content)
	}
}

func TestPruneToolOutputs_FullReadCoversEarlierPrefixRead(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		call("a", "read_file", `{"path":"x.go","limit":200}`), result("a", 21_000),
		call("b", "read_file", `{"path":"x.go"}`), result("b", 3_000),
	}
	out, _, ok := PruneToolOutputs(msgs)
	if !ok || !IsPrunedPlaceholder(out[2].Content) {
		t.Fatal("a later full read should supersede an earlier prefix read")
	}
}

func TestPruneToolOutputs_ExemptAndErrorResultsKept(t *testing.T) {
	msgs := []adapter.Message{{Role: adapter.RoleUser, Content: "task"}}
	msgs = append(msgs, call("s", "Skill", `{"skill":"x"}`), result("s", 25_000))
	msgs = append(msgs, call("e", "run_bash", `{}`))
	msgs = append(msgs, adapter.Message{Role: adapter.RoleTool, ToolCallID: "e", Content: "error: " + strings.Repeat("y", 4*25_000)})
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, call(id, "run_bash", `{}`), result(id, 10_000))
	}
	out, _, ok := PruneToolOutputs(msgs)
	if !ok {
		t.Fatal("expected bulk to prune")
	}
	if IsPrunedPlaceholder(out[2].Content) {
		t.Error("Skill output must never be pruned")
	}
	if IsPrunedPlaceholder(out[4].Content) {
		t.Error("error output must never be pruned")
	}
}
