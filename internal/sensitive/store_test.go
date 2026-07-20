package sensitive

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sensitive-roots.json")
}

func TestLoad_MissingFileIsEmptyNotError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(s.Roots) != 0 {
		t.Errorf("missing file yielded %d roots, want 0", len(s.Roots))
	}
	if s.Version != Version {
		t.Errorf("Version = %d, want %d", s.Version, Version)
	}
}

// A malformed store must NOT degrade to "nothing is sensitive" — that would
// turn a typo into a silent loss of the protection this package exists for.
func TestLoad_MalformedFileIsError(t *testing.T) {
	p := storePath(t)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("malformed store must error rather than read as empty")
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	p := storePath(t)
	s := &Store{}
	if _, err := s.Add("/repo/phi"); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Roots) != 1 || got.Roots[0].Path != "/repo/phi" {
		t.Fatalf("roundtrip roots = %+v, want one /repo/phi", got.Roots)
	}
	if got.Roots[0].MarkedAt.IsZero() {
		t.Error("MarkedAt should be stamped on Add")
	}
}

// Descendant inheritance is what makes marking a repo root sufficient no
// matter which subdirectory a session started in.
func TestContains_CoversDescendants(t *testing.T) {
	s := &Store{}
	if _, err := s.Add("/repo/phi"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/repo/phi", true},
		{"/repo/phi/sub", true},
		{"/repo/phi/sub/deep", true},
		{"/repo/phi-other", false}, // prefix sibling, not a descendant
		{"/repo", false},           // parent is not covered by a child marking
		{"/elsewhere", false},
	} {
		if got := s.Contains(tc.path); got != tc.want {
			t.Errorf("Contains(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestContains_NilStoreIsFalse(t *testing.T) {
	var s *Store
	if s.Contains("/anything") {
		t.Error("nil store should contain nothing")
	}
}

func TestAdd_IsIdempotent(t *testing.T) {
	s := &Store{}
	added, err := s.Add("/repo/phi")
	if err != nil || !added {
		t.Fatalf("first Add = %v, %v; want true, nil", added, err)
	}
	added, err = s.Add("/repo/phi")
	if err != nil || added {
		t.Errorf("second Add = %v, %v; want false, nil", added, err)
	}
	if len(s.Roots) != 1 {
		t.Errorf("duplicate Add produced %d roots, want 1", len(s.Roots))
	}
}

// Remove is exact-match on purpose: removing a covered child must not silently
// strip protection from the broader parent entry that also covers it.
func TestRemove_ExactMatchOnly(t *testing.T) {
	s := &Store{}
	if _, err := s.Add("/repo/phi"); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Remove("/repo/phi/sub")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("removing a descendant should not match the parent entry")
	}
	if !s.Contains("/repo/phi/sub") {
		t.Error("descendant lost protection after a failed remove")
	}

	removed, _ = s.Remove("/repo/phi")
	if !removed {
		t.Error("exact-path remove should succeed")
	}
	if s.Contains("/repo/phi") {
		t.Error("root still sensitive after removal")
	}
}

func TestPaths_ReturnsEveryRoot(t *testing.T) {
	s := &Store{}
	for _, p := range []string{"/a", "/b"} {
		if _, err := s.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Paths()
	slices.Sort(got)
	if !slices.Equal(got, []string{"/a", "/b"}) {
		t.Errorf("Paths() = %v, want [/a /b]", got)
	}
	var nilStore *Store
	if p := nilStore.Paths(); p != nil {
		t.Errorf("nil store Paths() = %v, want nil", p)
	}
}

func TestClear(t *testing.T) {
	s := &Store{}
	if _, err := s.Add("/repo/phi"); err != nil {
		t.Fatal(err)
	}
	s.Clear()
	if len(s.Roots) != 0 {
		t.Errorf("Clear left %d roots", len(s.Roots))
	}
}
