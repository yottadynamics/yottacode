package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"~/foo/bar", filepath.Join(home, "foo", "bar")},
		{"/abs/path", "/abs/path"},
		{"rel/path", "rel/path"},
		{"~user/foo", "~user/foo"}, // ~user form is preserved
	}
	for _, c := range cases {
		if got := expandHome(c.in); got != c.want {
			t.Errorf("expandHome(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestRelPath_ExpandsTilde locks in the regression that the agent's
// "always allow" path used to write Write(~/foo/test.txt) literally,
// because relPath fed `~/...` through filepath.Join(cwd, ...) and got
// back a literal "~" segment under cwd.
func TestRelPath_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cwd := t.TempDir()
	got := relPath("~/foo/test.txt", cwd)
	want := filepath.ToSlash(filepath.Join(home, "foo", "test.txt"))
	if got != want {
		t.Errorf("relPath(~/foo/test.txt) = %q; want %q (no literal ~)", got, want)
	}
}

// TestEvaluate_TildePatternMatchesAbsoluteValue confirms that a user-
// authored Write(~/foo/**) rule matches paths the agent produces (which
// are home-expanded absolute paths after the relPath fix).
func TestEvaluate_TildePatternMatchesAbsoluteValue(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Write(~/foo/**)"}, nil, nil)
	p, _ := Load(cwd)

	hit := filepath.Join(home, "foo", "bar.txt")
	got := p.Evaluate("write_file", `{"path":"`+hit+`"}`)
	if got != Allow {
		t.Errorf("Write(~/foo/**) should match %s; got %v", hit, got)
	}

	miss := filepath.Join(home, "other", "bar.txt")
	got = p.Evaluate("write_file", `{"path":"`+miss+`"}`)
	if got != Default {
		t.Errorf("Write(~/foo/**) should NOT match %s; got %v", miss, got)
	}
}

// TestDeriveAllowRule_OutOfCwdAnchorsAtParent locks in that out-of-cwd
// writes derive a parent-directory glob — `Write(/home/me/Desktop/**)`
// for a write to `~/Desktop/notes.md` — so a single click handles the
// whole sibling tree. Hand-writing `Write(~/Desktop/**)` is also fine;
// the matcher expands either form.
func TestDeriveAllowRule_OutOfCwdAnchorsAtParent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cwd := t.TempDir()

	want := "Write(" + filepath.ToSlash(filepath.Join(home, "Desktop")) + "/**)"

	// Tilde input — relPath expands ~ before derive sees it.
	rule, ok := DeriveAllowRule("write_file", `{"path":"~/Desktop/notes.md"}`, cwd)
	if !ok {
		t.Fatalf("DeriveAllowRule(~/Desktop/notes.md) should succeed")
	}
	if rule != want {
		t.Errorf("DeriveAllowRule(~/Desktop/notes.md) = %q; want %q", rule, want)
	}

	// Pre-resolved absolute — same expected rule.
	abs := filepath.Join(home, "Desktop", "other.md")
	rule2, ok2 := DeriveAllowRule("write_file", `{"path":"`+abs+`"}`, cwd)
	if !ok2 || rule2 != want {
		t.Errorf("DeriveAllowRule(%s) = %q ok=%v; want %q ok=true", abs, rule2, ok2, want)
	}
}

// TestDeriveAllowRule_SuppressedRoots verifies that always-allow is
// suppressed for system roots, the user's $HOME directly, and "/" —
// places where a one-click blanket grant would be a footgun.
func TestDeriveAllowRule_SuppressedRoots(t *testing.T) {
	cwd := t.TempDir()
	cases := []string{
		// POSIX / Linux roots
		"/etc/hosts",
		"/usr/local/bin/foo",
		"/var/log/syslog",
		"/proc/cpuinfo",
		"/foo.txt",
		// macOS roots
		"/System/Library/CoreServices/Foo",
		"/Library/Preferences/com.example.plist",
		"/Applications/Foo.app/Contents/Info.plist",
		"/Volumes/External/file.txt",
		"/private/etc/hosts",
		"/opt/homebrew/bin/foo",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cases = append(cases, filepath.Join(home, "directfile.txt"))
	}
	for _, p := range cases {
		if _, ok := DeriveAllowRule("write_file", `{"path":"`+p+`"}`, cwd); ok {
			t.Errorf("DeriveAllowRule(%q) should suppress; got ok=true", p)
		}
	}
}
