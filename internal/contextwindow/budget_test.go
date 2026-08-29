package contextwindow

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestDistributeBudget_UnderBudgetReturnsUnchanged(t *testing.T) {
	demand := []int{100, 4096, 4096, 2000}
	got := DistributeBudget(demand, 100_000, 256)
	if len(got) != len(demand) {
		t.Fatalf("expected %d allocations, got %d", len(demand), len(got))
	}
	for i, d := range demand {
		if got[i] != d {
			t.Errorf("index %d: expected unchanged demand %d, got %d", i, d, got[i])
		}
	}
}

func TestDistributeBudget_WaterFillsFairly(t *testing.T) {
	// Two small items well under a fair share, two large items competing
	// for the rest. The small items should pass through untouched; the
	// large items should split the leftover evenly.
	demand := []int{500, 500, 4096, 4096}
	got := DistributeBudget(demand, 4000, 100)

	if got[0] != 500 || got[1] != 500 {
		t.Fatalf("small items should pass through unchanged: got %v", got)
	}
	if got[2] != got[3] {
		t.Fatalf("large items should split the remaining budget evenly: got %v", got)
	}
	sum := got[0] + got[1] + got[2] + got[3]
	if sum > 4000 {
		t.Fatalf("sum %d exceeds totalBudget 4000: %v", sum, got)
	}
	if got[2] <= 500 {
		t.Fatalf("large items' cap should exceed the small items' demand once they yield slack: got %v", got)
	}
}

func TestDistributeBudget_FloorNeverViolatedUnlessDemandSmaller(t *testing.T) {
	// Many oversized items, tiny budget: an equal share would fall below
	// the floor, so every item should be pushed to floor exactly (never
	// below it), except a genuinely small demand which stays as-is.
	demand := []int{50, 4096, 4096, 4096, 4096, 4096}
	got := DistributeBudget(demand, 500, 256)

	if got[0] != 50 {
		t.Fatalf("an already-small demand must never be truncated below its own size: got %d", got[0])
	}
	for i := 1; i < len(demand); i++ {
		if got[i] < 256 {
			t.Fatalf("index %d: allocation %d fell below floor 256: %v", i, got[i], got)
		}
		if got[i] != 256 {
			t.Fatalf("index %d: expected exactly floor (256) under a starved budget, got %d: %v", i, got[i], got)
		}
	}
}

// TestDistributeBudget_ManyBelowFloorItemsMayExceedBudget pins the
// documented edge case where the sum guarantee cannot hold: many items
// each individually below floor, whose COMBINED demand alone exceeds
// totalBudget. Each item keeps its full demand (protected by (2)) rather
// than being shrunk further, so the returned sum legitimately exceeds
// totalBudget — this is accepted, not a bug; asserting it here makes the
// trade-off a tested contract instead of a silent surprise.
func TestDistributeBudget_ManyBelowFloorItemsMayExceedBudget(t *testing.T) {
	demand := make([]int, 40)
	for i := range demand {
		demand[i] = 200 // below floor (256), so protected in full
	}
	got := DistributeBudget(demand, 4000, 256)

	sum := 0
	for i, v := range got {
		if v != 200 {
			t.Fatalf("index %d: already-small demand must be preserved in full, got %d", i, v)
		}
		sum += v
	}
	if sum <= 4000 {
		t.Fatalf("expected this construction to demonstrate sum > totalBudget (documented trade-off), got sum=%d", sum)
	}
}

func TestDistributeBudget_EmptyDemand(t *testing.T) {
	if got := DistributeBudget(nil, 1000, 100); got != nil {
		t.Fatalf("nil demand should return nil, got %v", got)
	}
	if got := DistributeBudget([]int{}, 1000, 100); got != nil {
		t.Fatalf("empty demand should return nil, got %v", got)
	}
}

func TestDistributeBudget_SumBelowBudgetWhenFeasible(t *testing.T) {
	// floor * n <= totalBudget in every case below, so a feasible
	// allocation exists and the sum must respect totalBudget.
	cases := []struct {
		demand      []int
		totalBudget int
		floor       int
	}{
		{[]int{4096, 4096, 4096, 4096, 4096}, 10_000, 256},
		{[]int{1, 2, 3, 100_000, 200_000, 300_000}, 50_000, 100},
		{[]int{4096, 4096, 4096, 4096, 4096, 4096, 4096, 4096, 4096, 4096}, 20_000, 256},
	}
	for i, c := range cases {
		got := DistributeBudget(c.demand, c.totalBudget, c.floor)
		sum := 0
		for _, v := range got {
			sum += v
		}
		if sum > c.totalBudget {
			t.Errorf("case %d: sum %d exceeds totalBudget %d: %v", i, sum, c.totalBudget, got)
		}
		for j, v := range got {
			if v > c.demand[j] {
				t.Errorf("case %d, index %d: allocation %d exceeds demand %d", i, j, v, c.demand[j])
			}
		}
	}
}

// TestToolBudgetCaps_ManyOversizedToolMessagesFitBudget is the direct,
// package-level regression test for the shared algorithm both
// internal/agent's capRetainedToolMessages and internal/tui's
// capRetainedToolBudget delegate to: many tool messages individually
// above ceiling, whose combined size must fit budgetTokens even though a
// fixed per-message cap alone would not bound their sum.
func TestToolBudgetCaps_ManyOversizedToolMessagesFitBudget(t *testing.T) {
	const n = 30
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "ask"},
	}
	toolContent := strings.Repeat("z", 20_000) // ~5000 tokens, above the 4096 ceiling
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			adapter.Message{Role: adapter.RoleAssistant, Content: "step"},
			adapter.Message{Role: adapter.RoleTool, Content: toolContent},
		)
	}

	const budget, ceiling, floor = 20_000, 4096, 256
	idxs, raw, caps := ToolBudgetCaps(msgs, budget, ceiling, floor)

	if len(idxs) != n || len(raw) != n || len(caps) != n {
		t.Fatalf("expected %d tool messages tracked, got idxs=%d raw=%d caps=%d", n, len(idxs), len(raw), len(caps))
	}
	sum := 0
	for k := range idxs {
		if caps[k] > raw[k] {
			t.Errorf("index %d: cap %d exceeds actual size %d (must never inflate)", k, caps[k], raw[k])
		}
		sum += caps[k]
	}
	if sum > budget {
		t.Fatalf("combined tool cap %d exceeds budget %d", sum, budget)
	}
}

func TestToolBudgetCaps_NoToolMessagesReturnsNil(t *testing.T) {
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "ask"},
		{Role: adapter.RoleAssistant, Content: "reply"},
	}
	idxs, raw, caps := ToolBudgetCaps(msgs, 1000, 4096, 256)
	if idxs != nil || raw != nil || caps != nil {
		t.Fatalf("expected all-nil for no tool messages, got idxs=%v raw=%v caps=%v", idxs, raw, caps)
	}
}

// TestToolBudgetCaps_SystemMessageExcludedFromOverhead guards the
// defensive RoleSystem skip: a system message inside msgs must not be
// debited against the tool budget (it's never itself retained by either
// caller), even though this shouldn't occur in practice for either real
// call site (system messages live before the retained tail begins).
func TestToolBudgetCaps_SystemMessageExcludedFromOverhead(t *testing.T) {
	withSystem := []adapter.Message{
		{Role: adapter.RoleSystem, Content: strings.Repeat("s", 4000)},
		{Role: adapter.RoleTool, Content: strings.Repeat("t", 4000)},
	}
	withoutSystem := []adapter.Message{
		{Role: adapter.RoleTool, Content: strings.Repeat("t", 4000)},
	}
	_, _, capsWith := ToolBudgetCaps(withSystem, 500, 4096, 256)
	_, _, capsWithout := ToolBudgetCaps(withoutSystem, 500, 4096, 256)
	if len(capsWith) != 1 || len(capsWithout) != 1 || capsWith[0] != capsWithout[0] {
		t.Fatalf("system message should not affect the tool budget: with=%v without=%v", capsWith, capsWithout)
	}
}
