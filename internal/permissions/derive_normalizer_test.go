package permissions

import "testing"

// Regression: a Write with an ABSOLUTE path inside a yottacode
// worktree used to produce a rule that baked the auto-generated
// worktree name into the pattern, polluting permissions.json with
// per-worktree rules. With the PathNormalizer injected, the name
// segment collapses to `*`, so one click yields a rule that covers
// every worktree of the same repo.
func TestDeriveAllowRule_NormalizesAbsoluteDescriptor(t *testing.T) {
	cwd := "/home/me/proj"
	normalize := func(p string) string {
		// Stand-in for worktree.NormalizeForRule: rewrites the name
		// segment of any path under ~/.yottacode/worktrees/<slug>/ to `*`.
		// Implemented here as a fixed substring swap so the permissions
		// package stays decoupled from internal/worktree.
		const prefix = "/home/me/.yottacode/worktrees/yottacode-1a2b3c4d/"
		const sub = prefix + "noble-hopping-salmon/"
		const wild = prefix + "*/"
		out := ""
		if len(p) >= len(sub) && p[:len(sub)] == sub {
			out = wild + p[len(sub):]
		} else {
			out = p
		}
		return out
	}

	abs := "/home/me/.yottacode/worktrees/yottacode-1a2b3c4d/noble-hopping-salmon/internal/agent/loop.go"
	rule, ok := DeriveAllowRule("write_file", `{"path":"`+abs+`"}`, cwd, normalize)
	if !ok {
		t.Fatalf("expected ok, got false; rule=%q", rule)
	}
	want := "Write(/home/me/.yottacode/worktrees/yottacode-1a2b3c4d/*/internal/agent/**)"
	if rule != want {
		t.Errorf("rule=%q\nwant=%q", rule, want)
	}
}

// A nil normalizer is identity — current callers (tests, anything not
// yet injecting worktree awareness) keep their existing behavior.
// Uses an out-of-cwd absolute path so the absolute-descriptor branch
// fires (in-cwd absolute paths get folded into a cwd-relative rule
// earlier in targetFor and never reach normalization).
func TestDeriveAllowRule_NilNormalizerIsIdentity(t *testing.T) {
	cwd := "/home/me/proj"
	abs := "/elsewhere/Desktop/notes.md"
	rule, ok := DeriveAllowRule("write_file", `{"path":"`+abs+`"}`, cwd, nil)
	if !ok {
		t.Fatalf("expected ok, got false; rule=%q", rule)
	}
	want := "Write(/elsewhere/Desktop/**)"
	if rule != want {
		t.Errorf("rule=%q\nwant=%q", rule, want)
	}
}
