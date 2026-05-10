package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cwd := "/cwd"
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"foo.txt", filepath.Join(cwd, "foo.txt")},
		{"/abs/path.txt", "/abs/path.txt"},
		{"~/foo.txt", filepath.Join(home, "foo.txt")},
		{"~", home},
	}
	for _, c := range cases {
		if got := resolvePath(cwd, c.in); got != c.want {
			t.Errorf("resolvePath(%q, %q) = %q; want %q", cwd, c.in, got, c.want)
		}
	}
}

// TestResolvePath_TildeNoLiteralSegment is the direct regression:
// previously `filepath.Join(cwd, "~/foo")` produced ".../cwd/~/foo",
// causing write_file to create a literal "~" directory under cwd.
func TestResolvePath_TildeNoLiteralSegment(t *testing.T) {
	got := resolvePath("/cwd", "~/foo.txt")
	if filepath.Base(filepath.Dir(got)) == "~" {
		t.Errorf("resolvePath produced a literal ~ segment: %q", got)
	}
}
