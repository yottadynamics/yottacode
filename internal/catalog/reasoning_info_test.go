package catalog

import "testing"

func TestReasoningInfo_UnknownModel(t *testing.T) {
	// A model the catalog doesn't describe must yield the zero values so
	// the adapter leaves reasoning at the provider default.
	maxOut, thinking := ReasoningInfo("definitely-not-a-real-model-xyz")
	if maxOut != 0 || thinking != nil {
		t.Errorf("ReasoningInfo(unknown) = (%d, %v), want (0, nil)", maxOut, thinking)
	}
}

func TestFindByID_RoundTripsCatalog(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Skip("embedded catalog is empty")
	}
	want := all[0]
	got, ok := FindByID(want.ID)
	if !ok {
		t.Fatalf("FindByID(%q) not found, but it's in All()", want.ID)
	}
	if got.ID != want.ID {
		t.Errorf("FindByID(%q) returned %q", want.ID, got.ID)
	}
	// ReasoningInfo must agree with the entry FindByID returns.
	maxOut, thinking := ReasoningInfo(want.ID)
	if maxOut != want.MaxOutput || thinking != want.Capabilities.Thinking {
		t.Errorf("ReasoningInfo(%q) = (%d, %v), want (%d, %v)",
			want.ID, maxOut, thinking, want.MaxOutput, want.Capabilities.Thinking)
	}
}
