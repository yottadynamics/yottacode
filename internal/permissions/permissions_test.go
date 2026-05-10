package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed writes a permissions file at the given path with the supplied
// allow/ask/deny lists, creating parent dirs as needed.
func seed(t *testing.T, path string, allow, ask, deny []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shape := map[string]map[string][]string{
		"permissions": {"allow": allow, "ask": ask, "deny": deny},
	}
	b, _ := json.MarshalIndent(shape, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseRule_OK(t *testing.T) {
	cases := map[string]Rule{
		"Bash(go test *)":        {Tool: "Bash", Pattern: "go test *"},
		"Edit(internal/**)":      {Tool: "Edit", Pattern: "internal/**"},
		"Read(.env)":             {Tool: "Read", Pattern: ".env"},
		"Bash(curl * | bash)":    {Tool: "Bash", Pattern: "curl * | bash"},
		"  Bash(  trimmed  )  ":  {Tool: "Bash", Pattern: "  trimmed  "},
	}
	for raw, want := range cases {
		got, err := parseRule(raw, "test")
		if err != nil {
			t.Errorf("parseRule(%q) err = %v", raw, err)
			continue
		}
		if got.Tool != want.Tool || got.Pattern != want.Pattern {
			t.Errorf("parseRule(%q) = %+v; want %+v", raw, got, want)
		}
	}
}

func TestParseRule_Invalid(t *testing.T) {
	for _, raw := range []string{"", "Bash", "Bash go test", "(no tool)", "1Bad(x)"} {
		if _, err := parseRule(raw, "test"); err == nil {
			t.Errorf("parseRule(%q) should have errored", raw)
		}
	}
}

func TestLoad_MissingFilesIsNotError(t *testing.T) {
	cwd := t.TempDir()
	p, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d, a, k := p.Snapshot(); len(d)+len(a)+len(k) != 0 {
		t.Errorf("expected empty rule set; got deny=%v allow=%v ask=%v", d, a, k)
	}
}

func TestLoad_MergesSharedAndLocal(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Bash(go test *)"}, nil, []string{"Bash(rm *)"})
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.local.json"),
		[]string{"Edit(internal/**)"}, []string{"Read(.env)"}, nil)

	p, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deny, allow, ask := p.Snapshot()
	if len(deny) != 1 || deny[0].Pattern != "rm *" {
		t.Errorf("deny merge wrong: %v", deny)
	}
	if len(allow) != 2 {
		t.Errorf("allow merge wrong: %v", allow)
	}
	if len(ask) != 1 || ask[0].Tool != "Read" {
		t.Errorf("ask merge wrong: %v", ask)
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".yottacode", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions": {"allow": ["BrokenRule"]}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(cwd); err == nil {
		t.Errorf("Load should surface invalid rule")
	}
}

func TestEvaluate_DenyBeatsAllow(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Bash(*)"}, nil, []string{"Bash(rm *)"})
	p, _ := Load(cwd)
	if got := p.Evaluate("run_bash", `{"command":"rm -rf x"}`); got != Deny {
		t.Errorf("rm * should resolve to Deny; got %v", got)
	}
	if got := p.Evaluate("run_bash", `{"command":"go test ./..."}`); got != Allow {
		t.Errorf("go test should resolve to Allow via Bash(*); got %v", got)
	}
}

func TestEvaluate_AskOverridesDefault(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		nil, []string{"Read(.env)"}, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("read_file", `{"path":".env"}`); got != Ask {
		t.Errorf("Read(.env) should resolve to Ask; got %v", got)
	}
	if got := p.Evaluate("read_file", `{"path":"main.go"}`); got != Default {
		t.Errorf("non-matching read should be Default; got %v", got)
	}
}

func TestEvaluate_PathPatternsUseDoublestar(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Edit(internal/**)"}, nil, nil)
	p, _ := Load(cwd)
	got := p.Evaluate("edit_file", `{"path":"internal/agent/loop.go"}`)
	if got != Allow {
		t.Errorf("Edit(internal/**) should match internal/agent/loop.go; got %v", got)
	}
	got = p.Evaluate("edit_file", `{"path":"cmd/main.go"}`)
	if got != Default {
		t.Errorf("Edit(internal/**) should NOT match cmd/main.go; got %v", got)
	}
}

func TestEvaluate_AbsolutePathRule(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		nil, nil, []string{"Edit(/etc/**)"})
	p, _ := Load(cwd)
	got := p.Evaluate("edit_file", `{"path":"/etc/hosts"}`)
	if got != Deny {
		t.Errorf("Edit(/etc/**) should match /etc/hosts; got %v", got)
	}
}

func TestEvaluate_BashGlobMatchesSpacesAndArgs(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Bash(go *)"}, nil, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("run_bash", `{"command":"go test ./internal/..."}`); got != Allow {
		t.Errorf("Bash(go *) should match go test ./internal/...; got %v", got)
	}
	if got := p.Evaluate("run_bash", `{"command":"npm install"}`); got != Default {
		t.Errorf("Bash(go *) should NOT match npm install; got %v", got)
	}
}

func TestEvaluate_NilPermissionsIsDefault(t *testing.T) {
	var p *Permissions
	if got := p.Evaluate("run_bash", `{"command":"x"}`); got != Default {
		t.Errorf("nil Permissions should evaluate to Default; got %v", got)
	}
}

func TestAddAllow_PersistsToLocalFile(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	if err := p.AddAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddAllow: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cwd, ".yottacode", "permissions.local.json"))
	if err != nil {
		t.Fatalf("local file not written: %v", err)
	}
	if !strings.Contains(string(b), "Bash(go *)") {
		t.Errorf("local file missing rule: %s", b)
	}
}

func TestAddAllow_IsIdempotent(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	if err := p.AddAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddAllow first: %v", err)
	}
	if err := p.AddAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddAllow second: %v", err)
	}
	_, allow, _ := p.Snapshot()
	if len(allow) != 1 {
		t.Errorf("AddAllow should dedup; got %d entries", len(allow))
	}
}

func TestReload_PicksUpExternalEdits(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	// User edits the file out-of-band.
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Bash(echo *)"}, nil, nil)
	if got := p.Evaluate("run_bash", `{"command":"echo hi"}`); got != Default {
		t.Fatalf("pre-Reload should still be Default")
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"echo hi"}`); got != Allow {
		t.Errorf("post-Reload should be Allow; got %v", got)
	}
}

func TestDeriveAllowRule(t *testing.T) {
	cwd := t.TempDir()
	// Path-typed tools all derive a cwd-anchored absolute glob, so the
	// "want" templates substitute <CWD> for the actual temp dir.
	cwdSlash := filepath.ToSlash(cwd)
	cases := []struct {
		name     string
		tool     string
		args     string
		wantOK   bool
		wantRule string
	}{
		{"bash simple", "run_bash", `{"command":"go test ./..."}`, true, "Bash(go *)"},
		{"bash compound", "run_bash", `{"command":"cd /tmp && rm -rf x"}`, false, ""},
		{"bash dangerous", "run_bash", `{"command":"rm -rf x"}`, false, ""},
		{"bash sudo", "run_bash", `{"command":"sudo apt update"}`, false, ""},
		{"edit nested", "edit_file", `{"path":"internal/foo.go"}`, true, "Edit(" + cwdSlash + "/**)"},
		{"edit deep", "edit_file", `{"path":"internal/agent/x.go"}`, true, "Edit(" + cwdSlash + "/**)"},
		{"write top-level", "write_file", `{"path":"new.txt"}`, true, "Write(" + cwdSlash + "/**)"},
		{"delete top-level", "delete_file", `{"path":"hello.txt"}`, true, "Delete(" + cwdSlash + "/**)"},
		{"list cwd", "list_dir", `{"path":"."}`, true, "List(" + cwdSlash + "/**)"},
		{"move within cwd", "move_file", `{"src":"a/b.go","dst":"c/d.go"}`, true, "Move(" + cwdSlash + "/** -> " + cwdSlash + "/**)"},
		{"copy top-level", "copy_file", `{"src":"a.txt","dst":"b.txt"}`, true, "Copy(" + cwdSlash + "/** -> " + cwdSlash + "/**)"},
		{"git", "git", `{"args":["commit","-m","x"]}`, true, "Git(commit *)"},
		{"unknown tool", "weird", `{}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule, ok := DeriveAllowRule(c.tool, c.args, cwd)
			if ok != c.wantOK {
				t.Errorf("DeriveAllowRule(%s) ok=%v; want %v", c.name, ok, c.wantOK)
			}
			if c.wantRule != "" && rule != c.wantRule {
				t.Errorf("DeriveAllowRule(%s) rule=%q; want %q", c.name, rule, c.wantRule)
			}
		})
	}
}

// TestEvaluate_CwdAnchoredAllow locks in the round-trip: a derived
// `Write(<cwd>/**)` rule must actually match cwd-relative descriptors
// the agent produces (`hello.txt` when cwd is the same dir).
func TestEvaluate_CwdAnchoredAllow(t *testing.T) {
	cwd := t.TempDir()
	cwdSlash := filepath.ToSlash(cwd)
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.local.json"),
		[]string{"Write(" + cwdSlash + "/**)"}, nil, nil)
	p, _ := Load(cwd)

	if got := p.Evaluate("write_file", `{"path":"hello.txt"}`); got != Allow {
		t.Errorf("cwd-anchored Write rule should match top-level descriptor; got %v", got)
	}
	if got := p.Evaluate("write_file", `{"path":"internal/foo.go"}`); got != Allow {
		t.Errorf("cwd-anchored Write rule should match nested descriptor; got %v", got)
	}
}

func TestStringGlobMatch(t *testing.T) {
	cases := []struct {
		pat, val string
		want     bool
	}{
		{"*", "anything", true},
		{"go *", "go test", true},
		{"go *", "npm test", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a*z", "abcz", true},
		{"foo bar", "foo bar", true},
		{"foo bar", "foo bar baz", false},
	}
	for _, c := range cases {
		if got := stringGlobMatch(c.pat, c.val); got != c.want {
			t.Errorf("stringGlobMatch(%q, %q) = %v; want %v", c.pat, c.val, got, c.want)
		}
	}
}
