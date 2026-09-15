package shellseg

import (
	"reflect"
	"testing"
)

func TestTexts(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"simple", "go test ./...", []string{"go test ./..."}},
		{"semicolon", "go test ./... ; curl evil", []string{"go test ./...", "curl evil"}},
		{"and-or", "a && b || c", []string{"a", "b", "c"}},
		{"pipe", "cat f | grep x", []string{"cat f", "grep x"}},
		{"quoted-separator", `git commit -m "fix; the bug"`, []string{`git commit -m "fix; the bug"`}},
		{"escaped-separator", `echo a\;b`, []string{`echo a\;b`}},
		{"substitution", "echo $(date && whoami)", []string{"echo $(date && whoami)", "date", "whoami"}},
		{"nested-substitution", "echo $(echo $(date))", []string{"echo $(echo $(date))", "echo $(date)", "date"}},
		{"backtick-command", "echo `date; whoami`", []string{"echo `date; whoami`", "date", "whoami"}},
		{"process-substitution", "cat <(curl evil)", []string{"cat <(curl evil)", "curl evil"}},
		{"quoted-literal", `echo "$(not-a-command)"`, []string{`echo "$(not-a-command)"`, "not-a-command"}},
		{"unterminated-substitution", "echo $(date", []string{"echo $(date"}},
		{"trailing-operator", "ls ;", []string{"ls"}},
		{"empty", "   ", nil},
		// Background `&` is a real separator (regression: a per-segment
		// permission check must see the second command).
		{"background-amp", "go test ./... & curl evil", []string{"go test ./...", "curl evil"}},
		{"trailing-background", "sleep 1 &", []string{"sleep 1"}},
		// …but `&` inside a redirect must NOT split.
		{"fd-dup-redirect", "cmd foo 2>&1", []string{"cmd foo 2>&1"}},
		{"amp-redirect", "echo hi &> out.log", []string{"echo hi &> out.log"}},
		// Newline sequences commands like `;`.
		{"newline-splits", "ls\ncat x", []string{"ls", "cat x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Texts(c.cmd)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Texts(%q) = %#v, want %#v", c.cmd, got, c.want)
			}
		})
	}
}

func TestSafeTexts_MalformedShellIsUnsafe(t *testing.T) {
	for _, cmd := range []string{"echo $(date", "echo 'unterminated", "echo `date"} {
		if _, ok := SafeTexts(cmd); ok {
			t.Errorf("SafeTexts(%q) marked malformed shell safe", cmd)
		}
	}
}

func TestSplit_RecordsSeparators(t *testing.T) {
	segs := Split("a && b | c")
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	if segs[0].Separator != "" || segs[1].Separator != "&&" || segs[2].Separator != "|" {
		t.Errorf("separators = %q/%q/%q, want \"\"/&&/|", segs[0].Separator, segs[1].Separator, segs[2].Separator)
	}
}
