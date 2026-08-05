package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// TestDemo_CardOutput is a throwaway visualization that renders every
// built-in tool's card so the output format can be reviewed at a glance.
// Run with: go test ./internal/tui/ -run TestDemo_CardOutput -v
//
// Skipped under -short (which CI uses) because the rendered output
// contains lines like "└ edited /abs/main.go: 1 replacement(s)" that
// GitHub Actions' Go problem-matcher heuristically parses as compile
// errors (path.go:line:message) and surfaces as spurious "Error:" PR
// annotations. The test still passes — it's just noise in CI. Run
// locally without -short (or with -run) to inspect card output.
func TestDemo_CardOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("demo visualization — run without -short or with -run TestDemo_CardOutput to inspect")
	}
	width := 80
	cases := []struct {
		label, toolName, preview, args, output string
		errored                                bool
	}{
		{"Bash success", "run_bash", "run_bash: go build ./...",
			`{"command":"go build ./..."}`,
			"exit=0\n--- stdout ---\n\n--- stderr ---\n", false},
		{"Bash with output + non-zero exit", "run_bash", "run_bash: rg --files | head -3",
			`{"command":"rg --files | head -3"}`,
			"exit=1\n--- stdout ---\nREADME.md\ngo.mod\ngo.sum\n--- stderr ---\nrg: some warning\n", false},
		{"Read", "read_file", "read_file(README.md)",
			`{"path":"README.md"}`,
			"# yottacode\n\nA terminal coding agent.\n", false},
		{"Read with offset/limit", "read_file", "read_file(big.txt, offset=100, limit=50)",
			`{"path":"big.txt","offset":100,"limit":50}`,
			"line 101\nline 102\nline 103\n", false},
		{"Write", "write_file", "write_file(.yottacode/YOTTACODE.md, 2083 bytes)",
			`{"path":".yottacode/YOTTACODE.md","content":"# yottacode\n..."}`,
			"wrote 2083 bytes to /repo/.yottacode/YOTTACODE.md", false},
		{"Edit", "edit_file", "edit_file(main.go, single)",
			`{"path":"main.go","old_string":"package foo","new_string":"package bar"}`,
			"edited /abs/main.go: 1 replacement(s)", false},
		{"Mkdir", "mkdir", "mkdir(.yottacode)",
			`{"path":".yottacode"}`,
			"created directory /repo/.yottacode", false},
		{"Move", "move_file", "move_file(a.go -> b.go)",
			`{"src":"a.go","dst":"b.go"}`,
			"moved a.go to b.go", false},
		{"Copy", "copy_file", "copy_file(a.go -> b.go)",
			`{"src":"a.go","dst":"b.go"}`,
			"copied a.go to b.go", false},
		{"Delete", "delete_file", "delete_file(stale.txt)",
			`{"path":"stale.txt"}`,
			"deleted file stale.txt", false},
		{"Read many", "read_many_files", "read_many_files(3 paths)",
			`{"paths":["a.go","b.go","c.go"]}`,
			"==> a.go <==\npackage a\n==> b.go <==\npackage b\n==> c.go <==\npackage c\n", false},
		{"List", "list_dir", "list_dir(.)",
			`{"path":"."}`,
			"d\tinternal\nd\tcmd\nf\tgo.mod\nf\tgo.sum\nf\tREADME.md\n", false},
		{"List (error)", "list_dir", "list_dir(.\\n)",
			"{\"path\":\".\\n\"}",
			"list_dir: open /.../.: no such file or directory", true},
		{"Tree", "list_project_structure", "list_project_structure(., depth=2)",
			`{"path":".","max_depth":2}`,
			"d\t4096\t2026-05-10T00:00:00Z\tinternal\nf\t91\t2026-05-10T00:00:00Z\tgo.mod\nd\t4096\t2026-05-10T00:00:00Z\tinternal/tui\n", false},
		{"Glob", "glob", "glob(**/*.go in .)",
			`{"pattern":"**/*.go","root":"."}`,
			"main.go\ninternal/tui/model.go\ninternal/tui/tool_card.go\n", false},
		{"Grep", "grep", `grep("renderToolCard" in internal/tui)`,
			`{"pattern":"renderToolCard","path":"internal/tui"}`,
			"internal/tui/tool_card.go:45:func renderToolCard(...) {\ninternal/tui/model.go:2175:m.appendLine(renderToolCard(...))\n", false},
		{"Fetch", "fetch_url", "fetch_url(https://google.com)",
			`{"url":"https://google.com"}`,
			"URL: https://google.com\nStatus: 200\nContent-Type: text/html; charset=iso-8859-1\nNote: response truncated to 65536 bytes\n\n<!doctype html><html>" + "[…snip 65 KiB of minified HTML…]" + "</html>", false},
		{"Patch", "apply_diff", "apply_diff\n--- a/main.go\n+++ b/main.go\n@@",
			`{"diff":"--- a/main.go\n+++ b/main.go\n..."}`,
			"applied 1 hunk to main.go", false},
		{"Git (safe)", "git", "$ git status -sb",
			`{"args":["status","-sb"]}`,
			"$ git status -sb\nexit=0\n--- stdout ---\n## main\n M README.md\n--- stderr ---\n", false},
		{"Git (destructive flag)", "git", "⚠ DESTRUCTIVE FLAG(S): --force\n  $ git push --force origin main",
			`{"args":["push","--force","origin","main"]}`,
			"$ git push --force origin main\nexit=0\n--- stdout ---\n--- stderr ---\nTo origin\n   abc..def main -> main\n", false},
		{"Git branch status", "git_branch_status", "git_branch_status()",
			`{}`,
			"branch=main\nupstream=origin/main\ndirty=false\nahead=0\nbehind=0\n", false},
		{"Git diff files", "git_diff_files", "git_diff_files(base=main, head=feature, paths=2)",
			`{"Base":"main","Head":"feature","Paths":["a.go","b.go"]}`,
			"diff --git a/a.go b/a.go\n@@ -1,3 +1,4 @@\n+new line\n", false},
		{"Git stage", "git_stage_files", "git_stage_files(a.go, b.go)",
			`{"paths":["a.go","b.go"]}`,
			"staged 2 file(s)", false},
		{"Git commit", "git_commit", `git_commit("feat: add things")`,
			`{"message":"feat: add things"}`,
			"committed: abc1234 feat: add things", false},
		{"Git log file", "git_log_file", "git_log_file(README.md)",
			`{"path":"README.md"}`,
			"abc1234 update README\ndef5678 initial commit\n", false},
		{"Git blame", "git_blame_lines", "git_blame_lines(main.go:10-15)",
			`{"path":"main.go","Start":10,"End":15}`,
			"abc1234 main.go (alice 2026-05-10 10) line 10\n", false},
		{"Rollback", "rollback", "rollback(HEAD~1)\n  $ git reset --hard HEAD~1",
			`{"target":"HEAD~1"}`,
			"reset HEAD to abc1234", false},
		{"Test", "run_tests", "run_tests(go test ./... in .)",
			`{"command":"go test ./..."}`,
			"exit=0\n--- stdout ---\nok\tgithub.com/.../tui\t0.4s\n--- stderr ---\n", false},
		{"Memory save", "memory_save", `memory_save(scope=user, type=feedback, name=foo)`,
			`{"scope":"user","type":"feedback","name":"foo"}`,
			"saved memory user/foo", false},
		{"Memory forget", "memory_forget", `memory_forget(scope=user, name=foo)`,
			`{"scope":"user","name":"foo"}`,
			"forgot memory user/foo", false},
	}

	fmt.Println()
	for _, tc := range cases {
		fmt.Printf("─── %s ───\n", tc.label)
		// Duration 0: these render the common fast-call shape (no header tag).
		fmt.Println(stripANSI(renderToolCard(tc.toolName, tc.preview, tc.args, tc.output, tc.errored, width, "", 0)))
		fmt.Println()
	}

	// Error-tinted gutter + slow-call header tag. Rendered WITH color
	// (no stripANSI) so the red error frame and the right-aligned duration
	// are visible when eyeballing the demo. Clean cards stay neutral Dim.
	fmt.Println("─── Grouped cards ───")
	fmt.Println(stripANSI(renderGroupedToolCard([]groupedToolResult{
		{toolName: "read_file", preview: "read_file(a.go)", argsJSON: `{"path":"a.go"}`, output: "one\n"},
		{toolName: "glob", preview: "glob(**/*.go)", argsJSON: `{"pattern":"**/*.go"}`, output: "a.go\nb.go\n"},
	}, width, "")))
	fmt.Println()
	fmt.Println("─── Approval modal ───")
	approval := newTestModel(t)
	approval.width = width
	approval.approvalTool = "run_bash"
	approval.approvalPreview = "$ go test ./..."
	approval.approvalArgs = `{"command":"go test ./..."}`
	fmt.Println(stripANSI(renderApprovalModal(approval)))
	fmt.Println()
	fmt.Println("─── Plan approval ───")
	fmt.Println(stripANSI(renderPlanApprovalCard(width)))
	fmt.Println()
	fmt.Println("─── Path trust modal ───")
	pathTrust := newTestModel(t)
	pathTrust.width = width
	pathTrust.pathTrustReq = agent.PathTrustElevationNeeded{ToolName: "write_file", Path: "/tmp/outside/file.txt", Cwd: "/repo"}
	fmt.Println(stripANSI(renderPathTrustModal(pathTrust)))
	fmt.Println()
	fmt.Println("─── Loop card ───")
	fmt.Println(stripANSI(renderLoopCard(loopState{id: "loop-a", interval: 30 * time.Second, remaining: -1, payload: "/git-review-pr", expiresAt: time.Now().Add(loopDefaultTTL)}, width)))
	fmt.Println()
	fmt.Println("─── Hosted provider tool ───")
	fmt.Println(stripANSI(renderProviderToolCard("web_search", "searching", "", width)))
	fmt.Println()
	fmt.Println("─── Live plan card ───")
	fmt.Println(stripANSI(renderTodoCardFromTodos(samplePlan(), width)))
	fmt.Println()
	fmt.Println("─── LSP advisory ───")
	fmt.Println(stripANSI(renderLSPAdvisoryBox("LSP Code Intelligence", "Go", []string{"", "Go detected in this workspace — richer", "code intelligence is available by installing gopls.", "yottacode can ask to run the install command", "below through normal approval.", "", styleInlineCommand.Render("  go install golang.org/x/tools/gopls@latest"), "", styleMeta.Render("The session works without it; approving unlocks"), styleMeta.Render("go-to-def, live diagnostics, and symbol-aware review.")})))
	fmt.Println()

	// Error-tinted gutter + slow-call header tag. Rendered WITH color
	// (no stripANSI) so the red error frame and the right-aligned duration
	// are visible when eyeballing the demo. Clean cards stay neutral Dim.
	fmt.Println("─── Slow call (duration tag) ───")
	fmt.Println(renderToolCard("run_bash", "run_bash: go build ./...",
		`{"command":"go build ./..."}`,
		"exit=0\n--- stdout ---\n\n--- stderr ---\n", false, width, "", 4*time.Second))
	fmt.Println()
	fmt.Println("─── Failed call (red ┌ │ └ frame) ───")
	fmt.Println(renderToolCard("run_bash", "run_bash: ./flaky.sh",
		`{"command":"./flaky.sh"}`,
		"exit=1\n--- stdout ---\n--- stderr ---\nboom: exit status 1\n", true, width, "", 12*time.Second))
	fmt.Println()
}
