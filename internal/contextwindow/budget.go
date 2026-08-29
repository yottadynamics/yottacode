package contextwindow

import (
	"sort"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// DistributeBudget computes, for each item's demand (its size if
// unconstrained), a capped allocation such that:
//  1. no allocation exceeds that item's own demand (never inflate),
//  2. no allocation drops below floor UNLESS the item's own demand is
//     already below floor — an already-small item is never truncated
//     further just to make room for others, and keeps its FULL demand
//     regardless of totalBudget,
//  3. subject to (1) and (2), the sum of allocations is <= totalBudget
//     whenever achievable.
//
// Water-filling / max-min fair sharing: items whose demand already fits
// an equal share of the remaining budget pass through untouched, and
// their unused slack rolls forward to items that need it.
//
// Two distinct situations can make the returned sum exceed totalBudget —
// both are accepted trade-offs of protecting already-small items (2),
// not bugs:
//   - too many oversized (demand > floor) items for the budget: every one
//     gets pushed down to floor, and floor*n alone may exceed totalBudget.
//   - many items whose OWN demand already sits below floor: each keeps
//     its full (small) demand unconditionally per (2), so their combined
//     total is whatever it naturally is — DistributeBudget will not
//     shrink an already-small item further to help the sum fit, even
//     though nothing else in the budget technically prevented it.
//
// In both cases the alternative would be truncating some item to
// near-zero, which callers must not do; DistributeBudget always prefers
// leaving the sum over budget to producing a degenerate allocation.
//
// Exists to replace a fixed per-item cap applied independently to each
// oversized item: capping N items to the same fixed ceiling bounds each
// one but not their sum, so a turn with many moderately-large items (e.g.
// 30+ tool results) could still retain several times a caller's intended
// total budget. DistributeBudget bounds the sum instead, in the common
// case where oversized items dominate.
func DistributeBudget(demand []int, totalBudget, floor int) []int {
	n := len(demand)
	if n == 0 {
		return nil
	}

	sum := 0
	for _, d := range demand {
		sum += d
	}
	if sum <= totalBudget {
		return append([]int(nil), demand...)
	}

	// Ascending order so the item with the smallest demand is considered
	// first — the classic water-filling walk: an item that already fits
	// its equal share is left alone, and its slack rolls forward to the
	// remaining (larger) items.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return demand[order[a]] < demand[order[b]]
	})

	caps := make([]int, n)
	remaining := totalBudget
	for k, i := range order {
		count := n - k
		share := max(remaining/count, floor)
		if demand[i] <= share {
			caps[i] = demand[i]
			remaining -= demand[i]
			continue
		}
		// demand[i], and every later index (>= it in demand, by
		// ascending order), gets capped to the same share.
		for _, j := range order[k:] {
			caps[j] = min(demand[j], share)
		}
		break
	}
	return caps
}

// ToolBudgetCaps is the shared core of "retain this message tail, but keep
// its tool results within the remaining tail budget" — the exact scan +
// DistributeBudget wiring that internal/agent's compaction and internal/tui's
// /summarize compaction both need, factored out here after review flagged
// the two duplicating it independently (they still each own their own
// truncation marker text and mutation/return shape, which genuinely
// differ — only this numeric core is identical).
//
// Scans msgs for RoleTool messages; every other role's tokens (except
// RoleSystem, never itself retained by either caller) count as
// already-spent overhead subtracted from budgetTokens before allocating.
// ceiling pre-clips any single tool message's demand before water-filling
// runs, so one oversized payload never dominates the tail even when the
// overall budget happens to have slack for it; floor is the per-message
// floor passed straight to DistributeBudget.
//
// Returns, in encounter order, each tool message's index within msgs, its
// actual (uncapped) token size, and its computed cap — idxs/raw/caps are
// parallel slices of the same length. A caller truncates msgs[idxs[k]] to
// caps[k] only when raw[k] > caps[k]; raw is returned so that check never
// needs to re-estimate a message it already scanned once here. When the
// floor makes the budget infeasible, caps may still exceed the remaining
// tool budget; this mirrors DistributeBudget's documented trade-off of
// preserving a meaningful minimum fragment rather than truncating results
// to near-zero. All three slices are nil when msgs contains no tool messages.
func ToolBudgetCaps(msgs []adapter.Message, budgetTokens, ceiling, floor int) (idxs, raw, caps []int) {
	nonToolTokens := 0
	for i, m := range msgs {
		switch m.Role {
		case adapter.RoleTool:
			idxs = append(idxs, i)
		case adapter.RoleSystem:
		default:
			nonToolTokens += EstimateMessage(m)
		}
	}
	if len(idxs) == 0 {
		return nil, nil, nil
	}
	toolBudget := max(budgetTokens-nonToolTokens, 0)
	raw = make([]int, len(idxs))
	demand := make([]int, len(idxs))
	for k, i := range idxs {
		raw[k] = EstimateMessage(msgs[i])
		demand[k] = min(raw[k], ceiling)
	}
	caps = DistributeBudget(demand, toolBudget, floor)
	return idxs, raw, caps
}
