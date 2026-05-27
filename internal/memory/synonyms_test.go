package memory

import "testing"

func TestExpand_KnownSynonym(t *testing.T) {
	got := Expand("mock")
	if len(got) < 2 {
		t.Fatalf("Expand(mock) should return multiple synonyms; got %v", got)
	}
	want := map[string]bool{"mock": true, "fake": true, "stub": true, "doubl": true}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected synonym %q in group", s)
		}
	}
}

func TestExpand_UnknownToken(t *testing.T) {
	got := Expand("xyzzy")
	if len(got) != 1 || got[0] != "xyzzy" {
		t.Errorf("unknown token should return [xyzzy]; got %v", got)
	}
}

func TestExpand_Bidirectional(t *testing.T) {
	a := Expand("databas")
	b := Expand("sql")
	if len(a) != len(b) {
		t.Errorf("synonym groups should be identical: databas=%v sql=%v", a, b)
	}
}

func TestExpand_AllGroupsIndexed(t *testing.T) {
	for _, group := range synonymGroups {
		for _, member := range group {
			got := Expand(member)
			if len(got) != len(group) {
				t.Errorf("Expand(%q) returned %d items, want %d", member, len(got), len(group))
			}
		}
	}
}

func TestExpand_DevScenarios(t *testing.T) {
	cases := []struct {
		stem    string
		wantHas string
	}{
		{"test", "spec"},
		{"fake", "mock"},
		{"db", "databas"},
		{"deploy", "releas"},
		{"auth", "login"},
		{"permiss", "sandbox"},
		{"refactor", "migrat"},
		{"git", "commit"},
		{"api", "endpoint"},
		{"cach", "persist"},
	}
	for _, tc := range cases {
		got := Expand(tc.stem)
		found := false
		for _, s := range got {
			if s == tc.wantHas {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expand(%q) should contain %q; got %v", tc.stem, tc.wantHas, got)
		}
	}
}
