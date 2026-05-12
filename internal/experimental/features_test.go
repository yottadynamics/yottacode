package experimental

import (
	"strings"
	"testing"
)

func TestSet_DefaultEmptyHasNothingEnabled(t *testing.T) {
	s := NewSet()
	if s.IsEnabled(BackgroundSubagents) {
		t.Errorf("fresh Set should not have any features on")
	}
	if names := s.EnabledNames(); len(names) != 0 {
		t.Errorf("EnabledNames on fresh Set = %v, want empty", names)
	}
}

func TestSet_NilSafeIsEnabled(t *testing.T) {
	var s *Set
	if s.IsEnabled(BackgroundSubagents) {
		t.Errorf("nil Set should not panic and should return false")
	}
	if names := s.EnabledNames(); names != nil {
		t.Errorf("nil Set EnabledNames should be nil; got %v", names)
	}
}

func TestSet_EnableRecognizedNameTurnsOn(t *testing.T) {
	s := NewSet()
	s.Enable("background_subagents")
	if !s.IsEnabled(BackgroundSubagents) {
		t.Errorf("Enable(background_subagents) should turn the feature on")
	}
	if names := s.UnknownNames(); len(names) != 0 {
		t.Errorf("recognized name should not land in UnknownNames; got %v", names)
	}
}

func TestSet_EnableUnknownNameRecordsForWarning(t *testing.T) {
	s := NewSet()
	s.Enable("foo_made_up")
	if s.IsEnabled("foo_made_up") {
		t.Errorf("unknown name must not turn on any real feature")
	}
	got := s.UnknownNames()
	if len(got) != 1 || got[0] != "foo_made_up" {
		t.Errorf("UnknownNames = %v, want [foo_made_up]", got)
	}
}

func TestSet_ParseCommaListMergesNames(t *testing.T) {
	s := NewSet()
	s.Parse("background_subagents, unknown_thing ,,")
	if !s.IsEnabled(BackgroundSubagents) {
		t.Errorf("Parse should enable recognized names")
	}
	unknown := s.UnknownNames()
	if len(unknown) != 1 || unknown[0] != "unknown_thing" {
		t.Errorf("UnknownNames = %v, want [unknown_thing]", unknown)
	}
}

func TestSet_ParseEmptyStringIsNoOp(t *testing.T) {
	s := NewSet()
	s.Parse("")
	s.Parse(",,,")
	if len(s.EnabledNames()) != 0 || len(s.UnknownNames()) != 0 {
		t.Errorf("empty / comma-only Parse should be a no-op")
	}
}

func TestRecognizedMatchesAll(t *testing.T) {
	// Every name in All() must be Recognized; this guards against
	// accidental drift between the registry and the lookup helper.
	for _, f := range All() {
		if !Recognized(string(f)) {
			t.Errorf("All() returned %q but Recognized(%q) is false", f, f)
		}
		if Description(f) == "" {
			t.Errorf("All() returned %q but Description(%q) is empty", f, f)
		}
	}
}

func TestDescriptionReturnsEmptyForUnknown(t *testing.T) {
	if got := Description(Feature("graduated_feature_no_longer_in_registry")); got != "" {
		t.Errorf("Description for unknown name should be \"\"; got %q", got)
	}
}

func TestSet_EnabledNamesSortedAlphabetically(t *testing.T) {
	// Confirm the output is sorted so the status-bar render is
	// stable. Add a synthetic Feature for the test if/when we have
	// more than one; until then, just confirm single-entry works.
	s := NewSet()
	s.Enable("background_subagents")
	got := s.EnabledNames()
	want := []string{"background_subagents"}
	if !equalStrings(got, want) {
		t.Errorf("EnabledNames = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSet_EnableTrimsWhitespace(t *testing.T) {
	s := NewSet()
	s.Enable("  background_subagents  ")
	if !s.IsEnabled(BackgroundSubagents) {
		t.Errorf("Enable should trim whitespace from names")
	}
}

func TestSet_EnableEmptyStringIsNoOp(t *testing.T) {
	s := NewSet()
	s.Enable("")
	s.Enable("   ")
	if len(s.UnknownNames()) != 0 {
		t.Errorf("empty name should be silently ignored, not stored as unknown")
	}
}

func TestSet_DescriptionContentSanity(t *testing.T) {
	desc := Description(BackgroundSubagents)
	if !strings.Contains(desc, "Background subagents") {
		t.Errorf("description should mention the feature; got %q", desc)
	}
}
