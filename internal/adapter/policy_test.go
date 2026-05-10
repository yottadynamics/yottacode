package adapter

import (
	"reflect"
	"testing"
)

func TestFallbackChain_PreservesDeclaredOrder(t *testing.T) {
	in := []Candidate{
		{Label: "a"},
		{Label: "b"},
		{Label: "c"},
	}
	got := FallbackChain{}.Order(in)
	want := []string{"a", "b", "c"}
	if !labelsEqual(got, want) {
		t.Errorf("FallbackChain.Order labels = %v, want %v", labelsOf(got), want)
	}
}

func TestFallbackChain_DoesNotMutateInput(t *testing.T) {
	in := []Candidate{{Label: "a"}, {Label: "b"}}
	snapshot := append([]Candidate(nil), in...)
	_ = FallbackChain{}.Order(in)
	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("FallbackChain mutated input slice: before=%v after=%v", snapshot, in)
	}
}

func TestCheapFirst_SortsByTier(t *testing.T) {
	in := []Candidate{
		{Label: "expensive-a", Tier: TierExpensive},
		{Label: "balanced-a", Tier: TierBalanced},
		{Label: "cheap-a", Tier: TierCheap},
		{Label: "unspecified-a"},
	}
	got := CheapFirst{}.Order(in)
	want := []string{"cheap-a", "balanced-a", "expensive-a", "unspecified-a"}
	if !labelsEqual(got, want) {
		t.Errorf("CheapFirst.Order labels = %v, want %v", labelsOf(got), want)
	}
}

func TestCheapFirst_StableWithinSameTier(t *testing.T) {
	in := []Candidate{
		{Label: "cheap-1", Tier: TierCheap},
		{Label: "cheap-2", Tier: TierCheap},
		{Label: "cheap-3", Tier: TierCheap},
	}
	got := CheapFirst{}.Order(in)
	want := []string{"cheap-1", "cheap-2", "cheap-3"}
	if !labelsEqual(got, want) {
		t.Errorf("CheapFirst.Order labels = %v, want %v (declared order should win within a tier)", labelsOf(got), want)
	}
}

func TestCheapFirst_UnspecifiedSortsLast(t *testing.T) {
	in := []Candidate{
		{Label: "unspec"},
		{Label: "expensive", Tier: TierExpensive},
	}
	got := CheapFirst{}.Order(in)
	want := []string{"expensive", "unspec"}
	if !labelsEqual(got, want) {
		t.Errorf("CheapFirst.Order labels = %v, want %v", labelsOf(got), want)
	}
}

func TestCheapFirst_DoesNotMutateInput(t *testing.T) {
	in := []Candidate{
		{Label: "expensive", Tier: TierExpensive},
		{Label: "cheap", Tier: TierCheap},
	}
	snapshot := append([]Candidate(nil), in...)
	_ = CheapFirst{}.Order(in)
	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("CheapFirst mutated input slice: before=%v after=%v", snapshot, in)
	}
}

func TestPolicyName_ContractMatchesEvent(t *testing.T) {
	if name := (FallbackChain{}).Name(); name != "fallback-chain" {
		t.Errorf("FallbackChain.Name = %q, want %q", name, "fallback-chain")
	}
	if name := (CheapFirst{}).Name(); name != "cheap-first" {
		t.Errorf("CheapFirst.Name = %q, want %q", name, "cheap-first")
	}
}

func labelsOf(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Label
	}
	return out
}

func labelsEqual(got []Candidate, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Label != want[i] {
			return false
		}
	}
	return true
}
