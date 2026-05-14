package usercmd

import (
	"os"
	"sort"
	"testing"
)

// TestUserDirSmoke loads the real ~/.yottacode/commands/ directory and
// asserts every Tier 1 command we just dropped in resolves correctly:
// right namespaced name, frontmatter parsed, body non-empty. Skipped
// when HOME isn't /home/ppetkov so it doesn't run in CI or on other
// machines — this is a local sanity test that runs against the actual
// dotfiles, not a fixture.
//
// The git workflows (commit-message, create-pr) moved to procedural
// built-ins (/git-commit, /git-create-pr) and are not expected in
// user scope anymore. Only the check:* family remains as markdown
// starter kit.
func TestUserDirSmoke(t *testing.T) {
	if os.Getenv("HOME") != "/home/ppetkov" {
		t.Skip("smoke test runs only against ppetkov's local ~/.yottacode/commands/")
	}
	cmds, errs := Load("")
	for _, e := range errs {
		t.Logf("load notice: %v", e)
	}

	want := map[string]struct {
		argHint string
	}{
		"check:review": {argHint: "[base-branch]"},
		"check:verify": {argHint: "[task-or-hint]"},
	}

	got := map[string]Command{}
	for _, c := range cmds {
		got[c.Name] = c
	}

	missing := []string{}
	for name := range want {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("missing commands: %v\nloaded names: %v", missing, namesOf(cmds))
	}

	for name, expect := range want {
		c := got[name]
		if c.ArgHint != expect.argHint {
			t.Errorf("/%s: ArgHint = %q, want %q", name, c.ArgHint, expect.argHint)
		}
		if c.Description == "" {
			t.Errorf("/%s: empty Description (frontmatter not parsed?)", name)
		}
		if len(c.Body) < 50 {
			t.Errorf("/%s: body suspiciously short (%d bytes)", name, len(c.Body))
		}
		if c.Scope != ScopeUser {
			t.Errorf("/%s: Scope = %q, want user", name, c.Scope)
		}
	}

	// Substitution smoke: /check:verify with a sample $ARGUMENTS
	// expansion should land the user's task in the body.
	resolved := Substitute(got["check:verify"].Body, "fix the off-by-one bug")
	if !contains(resolved, "fix the off-by-one bug") {
		t.Errorf("$ARGUMENTS not substituted into /check:verify body")
	}
}

func namesOf(cmds []Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
