package permissions

import (
	"path/filepath"
	"testing"
)

// applyDiffArgs builds the JSON the agent would produce for an
// apply_diff call touching the given relative paths.
func applyDiffArgs(paths ...string) string {
	var diff string
	for _, p := range paths {
		diff += "--- a/" + p + "\n+++ b/" + p + "\n@@ -1 +1 @@\n-x\n+y\n"
	}
	return `{"diff":` + jsonString(diff) + `}`
}

// jsonString quotes s as a JSON string literal. Inlined to avoid a
// json.Marshal import in test helpers.
func jsonString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, string(r)...)
		}
	}
	out = append(out, '"')
	return string(out)
}

func newPerms(t *testing.T, cwd string, deny, allow, ask []string) *Permissions {
	t.Helper()
	p := LoadEmpty(cwd)
	for _, r := range deny {
		rule, err := parseRule(r, "test")
		if err != nil {
			t.Fatalf("parse %q: %v", r, err)
		}
		p.deny = append(p.deny, rule)
	}
	for _, r := range allow {
		rule, err := parseRule(r, "test")
		if err != nil {
			t.Fatalf("parse %q: %v", r, err)
		}
		p.allow = append(p.allow, rule)
	}
	for _, r := range ask {
		rule, err := parseRule(r, "test")
		if err != nil {
			t.Fatalf("parse %q: %v", r, err)
		}
		p.ask = append(p.ask, rule)
	}
	return p
}

// Regression: a Deny rule on a path the diff touches must fire even
// when the diff also touches uncovered paths. Pre-fix, apply_diff
// produced an empty descriptor and the deny never matched.
func TestEvaluate_ApplyDiff_DenyMatchesAnyPath(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, []string{"Edit(secrets/**)"}, nil, nil)
	args := applyDiffArgs("readme.md", "secrets/key.txt")
	if got := p.Evaluate("apply_diff", args); got != Deny {
		t.Errorf("expected Deny, got %v", got)
	}
}

// Allow on a multi-target call requires every path to match. A diff
// that mixes one allowed and one unknown path must NOT auto-allow.
func TestEvaluate_ApplyDiff_AllowRequiresAll(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, nil, []string{"Edit(internal/**)"}, nil)
	args := applyDiffArgs("internal/foo.go", "cmd/bar.go")
	if got := p.Evaluate("apply_diff", args); got != Default {
		t.Errorf("expected Default (modal still fires), got %v", got)
	}
	// All-covered diff DOES auto-allow.
	args = applyDiffArgs("internal/foo.go", "internal/bar.go")
	if got := p.Evaluate("apply_diff", args); got != Allow {
		t.Errorf("expected Allow, got %v", got)
	}
}

// Empty descriptor list (e.g. headerless diff) falls through to
// Default rather than vacuously matching every Allow rule.
func TestEvaluate_ApplyDiff_EmptyDescriptorsAreDefault(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, nil, []string{"Edit(*)"}, nil)
	args := `{"diff":"garbage no headers"}`
	if got := p.Evaluate("apply_diff", args); got != Default {
		t.Errorf("expected Default for headerless diff, got %v", got)
	}
}

// Deny precedence: a diff path that resolves to an absolute system
// path is matched by an absolute-path Deny rule.
func TestEvaluate_ApplyDiff_AbsolutePathDeny(t *testing.T) {
	cwd := t.TempDir()
	abs := filepath.Join(cwd, "internal", "secrets")
	p := newPerms(t, cwd, []string{"Edit(" + abs + "/**)"}, nil, nil)
	args := applyDiffArgs("internal/secrets/key.txt", "internal/foo.go")
	if got := p.Evaluate("apply_diff", args); got != Deny {
		t.Errorf("expected Deny, got %v", got)
	}
}
