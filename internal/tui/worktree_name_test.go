package tui

import "testing"

// Worktrees now live at ~/.yottacode/worktrees/<slug>/<name>/. The
// helper recognizes that layout and returns <name>; main-checkout or
// otherwise unrelated paths return "".
func TestWorktreeNameFromPath(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	tests := []struct {
		path string
		want string
	}{
		// Main checkout / unrelated paths return empty.
		{"/home/me/proj", ""},
		{"/home/me/proj/src/main.go", ""},
		{"/", ""},
		{"", ""},

		// Inside a yottacode worktree (new home location).
		{"/home/me/.yottacode/worktrees/proj-a1b2c3d4/feature-x", "feature-x"},
		{"/home/me/.yottacode/worktrees/proj-a1b2c3d4/feature-x/", "feature-x"},
		{"/home/me/.yottacode/worktrees/proj-a1b2c3d4/feature-x/internal/agent/loop.go", "feature-x"},

		// Names with hyphens, digits, and underscores.
		{"/home/me/.yottacode/worktrees/repo-12345678/fix_bug-123", "fix_bug-123"},

		// Lookalikes that should NOT match.
		{"/some/worktrees/foo", ""},                              // not under home
		{"/home/me/.yottacode/sessions/abc.json", ""},            // wrong subdir
		{"/home/me/.yottacode/worktrees/slug-only", ""},          // slug present, no name
		{"/home/me/.yottacode/worktrees", ""},                    // worktrees dir itself
		{"/home/other/.yottacode/worktrees/slug/name", ""},       // different home

		// Old in-repo path no longer recognized after the relocation.
		{"/home/me/proj/.yottacode/worktrees/feature-x", ""},
	}
	for _, tc := range tests {
		got := worktreeNameFromPath(tc.path)
		if got != tc.want {
			t.Errorf("worktreeNameFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
