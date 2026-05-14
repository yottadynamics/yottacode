package usercmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLoadDefaults_TwoEmbedded locks the starter-kit contract: the
// two Tier 1 markdown commands ship with the binary and parse
// cleanly. If a PR ever deletes one of the embedded .md files (or
// renames it incompatibly), this test fails immediately.
//
// The git workflows (/git-commit, /git-create-pr) moved from
// markdown defaults to procedural built-ins (cmd_git_commit.go,
// cmd_git_create_pr.go) — that's why this list is now two, not
// four. The TUI registry's drift test catches accidental removals
// of the built-ins.
func TestLoadDefaults_TwoEmbedded(t *testing.T) {
	cmds, errs := LoadDefaults()
	for _, e := range errs {
		t.Errorf("unexpected default-load error: %v", e)
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
	sort.Strings(missing)
	if len(missing) > 0 {
		names := make([]string, 0, len(cmds))
		for _, c := range cmds {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		t.Fatalf("missing defaults: %v\nloaded: %v", missing, names)
	}

	for name, expect := range want {
		c := got[name]
		if c.ArgHint != expect.argHint {
			t.Errorf("/%s: ArgHint = %q, want %q", name, c.ArgHint, expect.argHint)
		}
		if c.Description == "" {
			t.Errorf("/%s: empty Description (frontmatter parse?)", name)
		}
		if len(c.Body) < 50 {
			t.Errorf("/%s: body suspiciously short (%d bytes)", name, len(c.Body))
		}
		if c.Scope != ScopeDefault {
			t.Errorf("/%s: Scope = %q, want %q", name, c.Scope, ScopeDefault)
		}
		if !strings.HasPrefix(c.SourceFile, "defaults/") {
			t.Errorf("/%s: SourceFile = %q, want prefix 'defaults/'", name, c.SourceFile)
		}
	}
}

func TestLoadDefaults_DisplayPathRendersDefaultTag(t *testing.T) {
	cmds, _ := LoadDefaults()
	if len(cmds) == 0 {
		t.Fatal("expected at least one default")
	}
	if got := DisplayPath(cmds[0]); got != "(default)" {
		t.Errorf("DisplayPath for default = %q, want %q", got, "(default)")
	}
	// User-scope commands keep their absolute path.
	userCmd := Command{Scope: ScopeUser, SourceFile: "/home/x/.yottacode/commands/foo.md"}
	if got := DisplayPath(userCmd); got != "/home/x/.yottacode/commands/foo.md" {
		t.Errorf("DisplayPath for user = %q, want absolute path", got)
	}
}

// TestLoadAll_DefaultsMergedWithEmptyDisk: nothing on disk, defaults
// flow through as-is.
func TestLoadAll_DefaultsMergedWithEmptyDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmds, errs := LoadAll(t.TempDir())
	for _, e := range errs {
		t.Errorf("unexpected error: %v", e)
	}
	gotDefault := 0
	for _, c := range cmds {
		if c.Scope == ScopeDefault {
			gotDefault++
		}
	}
	if gotDefault < 2 {
		t.Errorf("expected >= 2 default commands, got %d", gotDefault)
	}
}

// TestLoadAll_UserShadowsDefault: a user file at the same path
// overrides the embedded default. The override is *silent* — no
// shadow-warning notice is emitted, because customizing a default is
// the documented use case.
//
// Uses /check:review as the override target because the git
// workflows moved to procedural built-ins; the markdown defaults
// that remain are check:review and check:verify.
func TestLoadAll_UserShadowsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".yottacode", "commands", "check")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	customBody := "MY CUSTOM REVIEW BODY"
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(customBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmds, errs := LoadAll(t.TempDir())

	// Should have no shadow warning — silent override.
	for _, e := range errs {
		if strings.Contains(e.Error(), "shadow") {
			t.Errorf("unexpected shadow warning for default override: %v", e)
		}
	}

	var got *Command
	for i := range cmds {
		if cmds[i].Name == "check:review" {
			got = &cmds[i]
		}
	}
	if got == nil {
		t.Fatal("/check:review not loaded after override")
	}
	if got.Scope != ScopeUser {
		t.Errorf("Scope = %q, want %q (user override should win)", got.Scope, ScopeUser)
	}
	if got.Body != customBody {
		t.Errorf("Body = %q, want override body %q", got.Body, customBody)
	}
}

// TestLoadAll_ProjectShadowsDefault: same as above but for project
// scope. Project files override defaults silently too.
func TestLoadAll_ProjectShadowsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".yottacode", "commands", "check")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte("PROJECT VERSION"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmds, errs := LoadAll(cwd)
	for _, e := range errs {
		if strings.Contains(e.Error(), "shadow") {
			t.Errorf("unexpected shadow warning for default override: %v", e)
		}
	}

	for _, c := range cmds {
		if c.Name == "check:review" {
			if c.Scope != ScopeProject {
				t.Errorf("Scope = %q, want project", c.Scope)
			}
			if c.Body != "PROJECT VERSION" {
				t.Errorf("Body = %q, want project version", c.Body)
			}
			return
		}
	}
	t.Fatal("/check:review not loaded")
}
