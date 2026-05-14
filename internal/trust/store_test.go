package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(filepath.Join(dir, "no-such-file.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if s == nil {
		t.Fatal("Load returned nil store on missing file")
	}
	if s.Version != Version {
		t.Errorf("Version = %d, want %d", s.Version, Version)
	}
	if len(s.Roots) != 0 {
		t.Errorf("Roots = %v, want empty", s.Roots)
	}
}

func TestLoadMalformedIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted-roots.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load malformed: want error, got nil")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	if _, err := s.Add("/home/me/repo-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("/home/me/repo-b"); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Roots) != 2 {
		t.Fatalf("Roots = %v, want 2", loaded.Roots)
	}
	if !loaded.Contains("/home/me/repo-a") {
		t.Error("Contains /home/me/repo-a: false, want true")
	}
}

func TestSaveCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "trusted-roots.json")
	if err := Save(path, &Store{}); err != nil {
		t.Fatalf("Save into missing dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}

func TestContainsSubfolderInherits(t *testing.T) {
	s := &Store{}
	if _, err := s.Add("/home/me/repo"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"/home/me/repo":             true,
		"/home/me/repo/":            true,
		"/home/me/repo/internal":    true,
		"/home/me/repo/internal/a":  true,
		"/home/me/other":            false,
		"/home/me":                  false,
		"/home/me/repo-sibling":     false, // prefix match without slash boundary
	}
	for in, want := range cases {
		if got := s.Contains(in); got != want {
			t.Errorf("Contains(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAddDedupes(t *testing.T) {
	s := &Store{}
	added, err := s.Add("/home/me/repo")
	if err != nil || !added {
		t.Fatalf("Add first: added=%v err=%v", added, err)
	}
	added, err = s.Add("/home/me/repo")
	if err != nil || added {
		t.Errorf("Add duplicate: added=%v err=%v, want added=false", added, err)
	}
	if len(s.Roots) != 1 {
		t.Errorf("Roots = %v, want 1", s.Roots)
	}
}

func TestAddCleansPath(t *testing.T) {
	s := &Store{}
	if _, err := s.Add("/home/me/repo/./../repo/."); err != nil {
		t.Fatal(err)
	}
	if got := s.Roots[0].Path; got != "/home/me/repo" {
		t.Errorf("stored path = %q, want %q", got, "/home/me/repo")
	}
}

func TestRemove(t *testing.T) {
	s := &Store{}
	s.Add("/home/me/a")
	s.Add("/home/me/b")
	ok, err := s.Remove("/home/me/a")
	if err != nil || !ok {
		t.Fatalf("Remove a: ok=%v err=%v", ok, err)
	}
	if len(s.Roots) != 1 || s.Roots[0].Path != "/home/me/b" {
		t.Errorf("after Remove: roots=%v, want [b]", s.Roots)
	}
	ok, _ = s.Remove("/home/me/never")
	if ok {
		t.Error("Remove nonexistent: want false")
	}
}

func TestClear(t *testing.T) {
	s := &Store{}
	s.Add("/home/me/a")
	s.Add("/home/me/b")
	s.Clear()
	if len(s.Roots) != 0 {
		t.Errorf("after Clear: roots=%v, want empty", s.Roots)
	}
}

func TestIsTrustedAllowPaths(t *testing.T) {
	s := &Store{}
	// cwd is NOT in the store, but it's under an --allow-paths entry
	if !IsTrusted(s, "/home/me/repo/internal", []string{"/home/me/repo"}) {
		t.Error("IsTrusted with cwd under allowPaths: false, want true")
	}
	// cwd is not in store and not under any allowPaths
	if IsTrusted(s, "/home/me/repo", []string{"/home/me/other"}) {
		t.Error("IsTrusted with cwd outside everything: true, want false")
	}
}

func TestIsTrustedEnvOverride(t *testing.T) {
	t.Setenv(EnvTrustAll, "1")
	if !IsTrusted(&Store{}, "/some/random/path", nil) {
		t.Error("YOTTACODE_TRUST_ALL=1 should satisfy gate")
	}
	t.Setenv(EnvTrustAll, "0")
	if IsTrusted(&Store{}, "/some/random/path", nil) {
		t.Error("YOTTACODE_TRUST_ALL=0 should not satisfy gate")
	}
}

func TestIsTrustedNilStore(t *testing.T) {
	if IsTrusted(nil, "/home/me/repo", nil) {
		t.Error("nil store + no allow paths should not be trusted")
	}
	if !IsTrusted(nil, "/home/me/repo", []string{"/home/me/repo"}) {
		t.Error("nil store + matching allow path should be trusted")
	}
}

func TestSaveAtomicLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	s.Add("/home/me/repo")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
