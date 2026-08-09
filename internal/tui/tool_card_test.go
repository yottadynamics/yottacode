package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/colorprofile"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// list_project_structure emits "marker\tsize\tmtime\trelpath" lines.
// The card renders only the relpath (with `/` appended to dirs),
// dropping the size and mtime columns the same way list_dir drops
// its marker column. Earlier the shaper only knew about list_dir, so
// list_project_structure output leaked through raw and the card
// looked like:
//
//	│ f    91    2026-05-03T02:09:50Z    go.mod
//
// instead of the intended one-name-per-line layout.
func TestToolCard_ListProjectStructureDropsMetaColumns(t *testing.T) {
	out := strings.Join([]string{
		"d\t4096\t2026-05-03T02:09:50Z\tinternal",
		"f\t91\t2026-05-03T02:09:50Z\tgo.mod",
		"f\t163\t2026-05-03T02:09:50Z\tgo.sum",
		"f\t5579\t2026-05-03T03:35:34Z\tmain.go",
	}, "\n") + "\n"
	body := toolBodyLines("list_project_structure", out, false, "")
	want := []string{"internal/", "go.mod", "go.sum", "main.go"}
	if len(body) != len(want) {
		t.Fatalf("body lines = %d, want %d: %v", len(body), len(want), body)
	}
	for i, w := range want {
		if body[i] != w {
			t.Errorf("body[%d] = %q, want %q", i, body[i], w)
		}
	}
	footer := stripANSI(toolFooter("list_project_structure", out, false, ""))
	if footer != "4 entries" {
		t.Errorf("footer = %q, want '4 entries'", footer)
	}
}

// Truncation markers the tool emits as their own line are preserved
// verbatim — the user wants to know "there's more we didn't show".
func TestToolCard_ListProjectStructureKeepsTruncationMarker(t *testing.T) {
	out := "f\t91\t2026-05-03T02:09:50Z\tgo.mod\n…[truncated at 5000 entries — narrow with path or lower max_depth]\n"
	body := toolBodyLines("list_project_structure", out, false, "")
	if len(body) != 2 {
		t.Fatalf("body lines = %d, want 2: %v", len(body), body)
	}
	if body[0] != "go.mod" {
		t.Errorf("body[0] = %q, want 'go.mod'", body[0])
	}
	if !strings.HasPrefix(body[1], "…[truncated") {
		t.Errorf("body[1] should preserve truncation marker; got %q", body[1])
	}
}

// list_dir output is a marker+name TSV; the card renders only names,
// with `/` appended to directories. Permissions, sizes, and
// timestamps are dropped — the model has the raw output, the user
// just needs to skim what's there.
func TestToolCard_ListDirDropsMarkerColumn(t *testing.T) {
	out := "d\tbin\nf\tREADME.md\nl\tlink\n"
	body := toolBodyLines("list_dir", out, false, "")
	want := []string{"bin/", "README.md", "link"}
	if len(body) != len(want) {
		t.Fatalf("body lines = %d, want %d: %v", len(body), len(want), body)
	}
	for i, w := range want {
		if body[i] != w {
			t.Errorf("body[%d] = %q, want %q", i, body[i], w)
		}
	}
	footer := stripANSI(toolFooter("list_dir", out, false, ""))
	if footer != "3 entries" {
		t.Errorf("footer = %q, want '3 entries'", footer)
	}
}

// run_bash output is exit=N\n--- stdout ---\n…\n--- stderr ---\n….
// Body shows stdout (and stderr, when present, after a separator);
// footer shows the exit code with a green/red tier.
func TestToolCard_RunBashSplitsStdoutStderr(t *testing.T) {
	out := "exit=0\n--- stdout ---\nhello\nworld\n--- stderr ---\n"
	body := toolBodyLines("run_bash", out, false, "")
	if !contains(body, "hello") || !contains(body, "world") {
		t.Errorf("body should contain stdout lines: %v", body)
	}
	footer := stripANSI(toolFooter("run_bash", out, false, ""))
	if footer != "exit 0" {
		t.Errorf("footer = %q, want 'exit 0'", footer)
	}
}

func TestRenderToolCard_GrepWrapsUnderMatchText(t *testing.T) {
	out := strings.Join([]string{
		"./internal/agent/prompt.go:30:  - lsp_status, lsp_symbols, lsp_document_symbols, lsp_document_highlights, lsp_selection_ranges, lsp_definition",
		"./internal/agent/events.go:205:// TurnInterrupted fires when the turn ended via user-initiated context cancellation",
	}, "\n") + "\n"
	got := stripANSI(renderToolCard(
		"grep",
		`grep("TurnInterrupted" in internal/agent)`,
		`{"pattern":"TurnInterrupted","path":"internal/agent","regex":false,"ignore_case":false}`,
		out,
		false,
		90,
		"",
		0,
	))
	if !strings.Contains(got, "│ ./internal/agent/prompt.go:30:") {
		t.Fatalf("first grep row should render structured prefix: %q", got)
	}
	if !strings.Contains(got, "│                                lsp_document_highlights") {
		t.Fatalf("wrapped grep continuation should hang-indent under text: %q", got)
	}
	if strings.Contains(got, "\n│ lsp_document_highlights") {
		t.Fatalf("grep continuation should not restart at column 0: %q", got)
	}
	if !strings.Contains(got, "└ 2 matches") {
		t.Fatalf("grep footer should still report match count: %q", got)
	}
}

func TestToolCard_SyntaxRangeHeaderAndFooter(t *testing.T) {
	out := "block\t/tmp/main.go:4:2-6:3\tlines=4-6\tanchor_read={}\nfunction run\t/tmp/main.go:3:1-7:2\tlines=3-7\tanchor_read={}\n"
	header := toolHeader("syntax_range", `{"path":"/tmp/main.go","line":4,"character":10}`, "", 100, "")
	if header != "Syntax(range /tmp/main.go:4:10)" {
		t.Fatalf("header = %q", header)
	}
	footer := stripANSI(toolFooter("syntax_range", out, false, ""))
	if footer != "2 matches" {
		t.Fatalf("footer = %q, want 2 matches", footer)
	}
}

func TestRenderSystemNoticeLine_UsesOneLineGrammar(t *testing.T) {
	got := stripANSI(renderSystemNoticeLine("auto", []string{
		"grep(\"auto mode\" in internal/tui)",
	}, "auto-mode", 88))
	if strings.Contains(got, "┌") || strings.Contains(got, "│") || strings.Contains(got, "└") {
		t.Fatalf("system notices should not render card gutters: %q", got)
	}
	if got != "○ auto · grep(\"auto mode\" in internal/tui) · auto-mode" {
		t.Fatalf("unexpected one-line notice: %q", got)
	}
}

func TestRenderCompactionNoticeLine_ShowsCompactRecallCommand(t *testing.T) {
	got := stripANSI(renderCompactionNoticeLine("87% → 23%", "/home/me/.yottacode/sessions/20260727-155559.693248-pre-summary-20260727-164818.296261277.json", nil, 100))
	want := "◇ context · compacted · 87% → 23% · full history saved · /recall 20260727-155559.693248"
	if got != want {
		t.Fatalf("unexpected compaction notice: %q", got)
	}
}

func TestToolCard_RunBashErrorExitColored(t *testing.T) {
	// run_bash always emits the format
	// "exit=N\n--- stdout ---\n<STDOUT>\n--- stderr ---\n<STDERR>".
	// With empty stdout there's still one blank line between the two
	// section markers — the `\n` after the empty stdout body.
	out := "exit=2\n--- stdout ---\n\n--- stderr ---\nboom\n"
	body := stripANSILines(toolBodyLines("run_bash", out, false, ""))
	if !contains(body, "── stderr ──") {
		t.Errorf("body should label the stderr section: %v", body)
	}
	if !contains(body, "boom") {
		t.Errorf("body should include the stderr line: %v", body)
	}
	footer := stripANSI(toolFooter("run_bash", out, false, ""))
	if footer != "exit 2" {
		t.Errorf("footer = %q, want 'exit 2'", footer)
	}
}

// read_file: empty body — dumping the first N lines of every file the
// model reads is visual noise; the footer carries the line+byte count
// so the user can still tell how much was read at a glance. The model
// of course gets the full content via ToolResult.Output regardless.
func TestRenderToolCard_ReadFileBodyIsEmpty(t *testing.T) {
	out := "package main\n\nimport \"fmt\"\n\nfunc main() {}\n"
	body := toolBodyLines("read_file", out, false, "")
	if len(body) != 0 {
		t.Errorf("read_file body should be empty, got %v", body)
	}
	footer := stripANSI(toolFooter("read_file", out, false, ""))
	if !strings.Contains(footer, "lines") || !strings.Contains(footer, "bytes") {
		t.Errorf("read_file footer should report lines + bytes; got %q", footer)
	}
}

func TestRenderGroupedToolCard_Reads(t *testing.T) {
	got := stripANSI(renderGroupedToolCard([]groupedToolResult{
		{toolName: "read_file", preview: "read_file(a.go)", argsJSON: `{"path":"a.go"}`, output: "one\n"},
		{toolName: "read_file", preview: "read_file(b.go)", argsJSON: `{"path":"b.go","offset":10,"limit":20}`, output: "two\nthree\n"},
	}, 100, ""))
	for _, want := range []string{
		"┌ Read · 2 calls",
		"Read(a.go) — 1 line · 4 bytes",
		"Read(b.go @ L10+20) — 2 lines · 10 bytes",
		"└ 2 calls · 3 lines · 14 bytes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("grouped read card missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderGroupedToolCard_Lists(t *testing.T) {
	got := stripANSI(renderGroupedToolCard([]groupedToolResult{
		{toolName: "list_dir", preview: "list_dir(.)", argsJSON: `{"path":"."}`, output: "d\tbin\nf\tREADME.md\n"},
		{toolName: "list_project_structure", preview: "list_project_structure(.)", argsJSON: `{"path":".","max_depth":2}`, output: "d\t4096\t2026-05-03T02:09:50Z\tinternal\nf\t91\t2026-05-03T02:09:50Z\tgo.mod\n"},
	}, 100, ""))
	for _, want := range []string{
		"┌ List · 2 calls",
		"List(.) — 2 entries",
		"Tree(., depth=2) — 2 entries",
		"└ 2 calls · 4 entries",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("grouped list card missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderGroupedToolCard_ReadManyFilesOmitsBogusLineCount(t *testing.T) {
	got := stripANSI(renderGroupedToolCard([]groupedToolResult{
		{toolName: "read_many_files", preview: "read_many_files(2 files)", argsJSON: `{"paths":["a.go","b.go"]}`, output: "==> a.go <==\none\n\n==> b.go <==\ntwo\n"},
		{toolName: "read_many_files", preview: "read_many_files(1 file)", argsJSON: `{"paths":["c.go"]}`, output: "==> c.go <==\nthree\n"},
	}, 100, ""))
	if !strings.Contains(got, "┌ Read · 2 calls") {
		t.Fatalf("expected grouped read_many_files card, got:\n%s", got)
	}
	if strings.Contains(got, "0 lines") {
		t.Fatalf("grouped read_many_files card should not claim zero lines:\n%s", got)
	}
	if !strings.Contains(got, "└ 2 calls · 54 bytes") {
		t.Fatalf("grouped read_many_files footer should fall back to bytes only, got:\n%s", got)
	}
}

func TestRenderGroupedToolCard_HangIndentsWrappedRows(t *testing.T) {
	got := stripANSI(renderGroupedToolCard([]groupedToolResult{
		{toolName: "read_file", preview: "read_file(internal/tui/very_long_component_name_that_wraps.go)", argsJSON: `{"path":"internal/tui/very_long_component_name_that_wraps.go","offset":159,"limit":150}`, output: "one\ntwo\n"},
	}, 52, ""))
	if !strings.Contains(got, "│   ") {
		t.Fatalf("wrapped grouped rows should hang-indent continuation lines:\n%s", got)
	}
	if strings.Contains(got, "\n│ bytes") {
		t.Fatalf("wrapped grouped row continuation restarted flat at the gutter:\n%s", got)
	}
}

func TestRenderGroupedToolCard_OverflowCopyIsTypeAware(t *testing.T) {
	readItems := make([]groupedToolResult, cardBodyLineCap+2)
	for i := range readItems {
		readItems[i] = groupedToolResult{toolName: "read_file", argsJSON: fmt.Sprintf(`{"path":"%d.go"}`, i), output: "x\n"}
	}
	readGot := stripANSI(renderGroupedToolCard(readItems, 100, ""))
	if !strings.Contains(readGot, "…2 more read calls") {
		t.Fatalf("read overflow should name read calls:\n%s", readGot)
	}

	listItems := make([]groupedToolResult, cardBodyLineCap+1)
	for i := range listItems {
		listItems[i] = groupedToolResult{toolName: "list_dir", argsJSON: fmt.Sprintf(`{"path":"dir-%d"}`, i), output: "f\tfile\n"}
	}
	listGot := stripANSI(renderGroupedToolCard(listItems, 100, ""))
	if !strings.Contains(listGot, "…1 more list call") {
		t.Fatalf("list overflow should name list calls:\n%s", listGot)
	}

	globItems := make([]groupedToolResult, cardBodyLineCap+3)
	for i := range globItems {
		globItems[i] = groupedToolResult{toolName: "glob", argsJSON: fmt.Sprintf(`{"pattern":"*.%d"}`, i), output: "a.go\n"}
	}
	globGot := stripANSI(renderGroupedToolCard(globItems, 100, ""))
	if !strings.Contains(globGot, "…3 more glob calls") {
		t.Fatalf("glob overflow should name glob calls:\n%s", globGot)
	}
}

// readFileFooter: line count uses singular when count == 1; truncation
// suffix when the tool's 512KiB read cap was hit; trailing newline isn't
// counted as a phantom extra line.
func TestReadFileFooter_Shape(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"single line, no trailing nl", "hello", "1 line · 5 bytes"},
		{"single line with nl", "hello\n", "1 line · 6 bytes"},
		{"multi line", "a\nb\nc\n", "3 lines · 6 bytes"},
		{"truncated", "x\n…[truncated]", "1 line · 1 bytes (truncated)"},
		{"empty", "", "0 lines · 0 bytes"},
	}
	for _, tc := range cases {
		got := readFileFooter(tc.out)
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// write_file: empty body. The footer drops the "to <abs/path>" tail
// because the header already names the path — no need to print it twice.
func TestToolCard_WriteFileFooterDropsRedundantPath(t *testing.T) {
	body := toolBodyLines("write_file", "wrote 70 bytes to /home/me/hello.go", false, "")
	if len(body) != 0 {
		t.Errorf("write_file body should be empty (footer carries all): %v", body)
	}
	footer := stripANSI(toolFooter("write_file", "wrote 70 bytes to /home/me/hello.go", false, ""))
	if footer != "wrote 70 bytes" {
		t.Errorf("footer = %q, want 'wrote 70 bytes' (path dropped — header carries it)", footer)
	}
}

// Errored output bypasses most per-tool body shapers and renders raw — the
// user needs to see the message verbatim. Recoverable edit/patch errors are
// the exception: they render concise guidance instead of dumping stale inputs.
func TestToolCard_ErroredOutputRendersRaw(t *testing.T) {
	body := toolBodyLines("list_dir", "list_dir: open /nope: no such file or directory", true, "")
	if len(body) != 1 || !strings.Contains(body[0], "no such file or directory") {
		t.Errorf("errored body should render raw: %v", body)
	}
	footer := stripANSI(toolFooter("list_dir", "list_dir: open /nope: no such file or directory", true, ""))
	if !strings.HasPrefix(footer, "✗ ") {
		t.Errorf("errored footer should be marked: %q", footer)
	}
}

func TestToolCard_ApplyDiffMalformedPatchIsConcise(t *testing.T) {
	out := `error: apply_diff: exit status 128; stderr="error: patch with only garbage at line 5\n" hint="context may not match the current file — re-read it and regenerate the diff" patch="diff --git a/internal/tui/tool_card.go b/internal/tui/tool_card.go\nindex 1111111..2222222 100644\n--- a/internal/tui/tool_card.go\n+++ b/internal/tui/tool_card.go\n@@\n-func old() {}\n+func new() {}\n"`
	body := toolBodyLines("apply_diff", out, true, "")
	if len(body) != 1 || body[0] != "malformed patch — use a valid unified diff with real hunk ranges" {
		t.Fatalf("apply_diff body should show concise diagnosis, got %v", body)
	}
	footer := stripANSI(toolFooter("apply_diff", out, true, ""))
	if footer != "recoverable: malformed patch" {
		t.Fatalf("apply_diff footer = %q", footer)
	}
}

func TestToolCard_ApplyDiffCorruptPatchIsConcise(t *testing.T) {
	out := `error: apply_diff: exit status 128; stderr="error: corrupt patch at line 138\n" hint="patch syntax is malformed — remove placeholder hunks like bare ` + "`@@`" + `, or regenerate a complete unified diff with valid hunk headers" patch="diff --git a/internal/agent/events.go b/internal/agent/events.go\nindex 6d57d55..def096b 100644\n--- a/internal/agent/events.go\n+++ b/internal/agent/events.go\n@@ -97,9 +97,12 @@ type ErrorEvent struct {\n type ApprovalAuto struct {\n\tToolName string\n\tPreview  string\n\tSource   string\n+\tRuleSource string\n }"`
	body := toolBodyLines("apply_diff", out, true, "")
	if len(body) != 1 || body[0] != "malformed patch — use a valid unified diff with real hunk ranges" {
		t.Fatalf("apply_diff body should show concise corrupt-patch diagnosis, got %v", body)
	}
	footer := stripANSI(toolFooter("apply_diff", out, true, ""))
	if footer != "recoverable: malformed patch" {
		t.Fatalf("apply_diff footer = %q", footer)
	}
	got := stripANSI(renderToolCard("apply_diff", "Patch(apply)", `{}`, out, true, 100, "", 0))
	if strings.Contains(got, "patch=\"") || strings.Contains(got, "RuleSource") {
		t.Fatalf("card should not dump raw corrupt patch payload: %q", got)
	}
	if !strings.Contains(got, "malformed patch") {
		t.Fatalf("card should classify corrupt patch as malformed: %q", got)
	}
}

// renderToolCard composes the full card. The header gutter (┌),
// per-line body gutter (│), and footer gutter (└) all need to be
// present so the user gets the unified shape.
func TestRenderToolCard_StructureInvariants(t *testing.T) {
	got := stripANSI(renderToolCard(
		"list_dir",
		"list_dir(.)",
		"",
		"d\tbin\nf\tmain.go\n",
		false,
		80,
		"",
		0,
	))
	if !strings.HasPrefix(got, "┌ list_dir(.)") {
		t.Errorf("card should open with `┌ <preview>`: %q", got)
	}
	if !strings.Contains(got, "│ bin/") || !strings.Contains(got, "│ main.go") {
		t.Errorf("card body should carry list_dir entries: %q", got)
	}
	if !strings.Contains(got, "└ 2 entries") {
		t.Errorf("card footer should show entry count: %q", got)
	}
}

// edit_file gets a structured diff body: a single-line header, then
// `- old` / `+ new` rows in the body (each gutter-prefixed with `│ `),
// then the standard footer. The previous shape stuffed the whole
// `edit_file(...)\n  - ...\n  + ...` triple into the header and broke
// header alignment. This guards against regression.
func TestRenderToolCard_EditFileRendersDiffBody(t *testing.T) {
	args := `{"path":"main.go","old_string":"package foo","new_string":"package bar"}`
	got := stripANSI(renderToolCard(
		"edit_file",
		"edit_file(main.go, single)",
		args,
		"edited /abs/main.go: 1 replacement(s)",
		false,
		80,
		"",
		0,
	))
	// Header: single line with the invocation, no embedded `-`/`+` rows.
	// toolHeader rewrites the raw preview into "Edit(path, scope)" once
	// argsJSON is available; the diff still goes in the body.
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasPrefix(header, "┌ Edit(main.go, single)") {
		t.Errorf("header should be the invocation only: %q", header)
	}
	if strings.Contains(header, "- package") || strings.Contains(header, "+ package") {
		t.Errorf("diff lines must not be inlined into the header: %q", header)
	}
	// Body: gutter-prefixed `- old` / `+ new` rows.
	if !strings.Contains(got, "│ - package foo") {
		t.Errorf("body should carry the gutter-prefixed `- old` row: %q", got)
	}
	if !strings.Contains(got, "│ + package bar") {
		t.Errorf("body should carry the gutter-prefixed `+ new` row: %q", got)
	}
	// Footer: the result message comes through unchanged.
	if !strings.Contains(got, "└ edited /abs/main.go: 1 replacement(s)") {
		t.Errorf("footer should carry the edit result: %q", got)
	}
}

func TestRenderToolCard_EditFileOldStringMissIsCompact(t *testing.T) {
	out := `error: edit_file: old_string not found in ./internal/tui/model.go — the closest line is 3910: "\t\tif worktreeSeg != \"\" {". Re-read the file and copy its exact current text (including whitespace) into old_string`
	got := stripANSI(renderToolCard(
		"edit_file",
		"edit_file(internal/tui/model.go, single)",
		`{"path":"internal/tui/model.go"}`,
		out,
		true,
		100,
		"",
		0,
	))
	if !strings.Contains(got, "stale edit target") {
		t.Fatalf("card should classify stale edit target: %q", got)
	}
	if !strings.Contains(got, "closest line: 3910") {
		t.Fatalf("card should retain closest-line hint: %q", got)
	}
	if strings.Count(got, "old_string not found") > 0 {
		t.Fatalf("card should not dump raw old_string error prose: %q", got)
	}
}

func TestToolCard_ApplyDiffStalePatchIsCompact(t *testing.T) {
	out := `error: apply_diff: exit status 1; stderr="error: patch failed: internal/tui/model.go:1707\nerror: internal/tui/model.go: patch does not apply\n" hint="patch headers were valid" patch="diff --git a/internal/tui/model.go b/internal/tui/model.go"`
	got := stripANSI(renderToolCard("apply_diff", "Patch(apply)", `{}`, out, true, 100, "", 0))
	if !strings.Contains(got, "stale patch context") {
		t.Fatalf("card should classify stale patch context: %q", got)
	}
	if !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("card should name failed file: %q", got)
	}
	if !strings.Contains(got, "prefer anchors=true") || !strings.Contains(got, "edit_anchored") {
		t.Fatalf("card should steer stale patches toward anchored recovery: %q", got)
	}
	if strings.Count(got, "patch failed") > 0 || strings.Count(got, "patch=\"") > 0 {
		t.Fatalf("card should not dump raw patch failure payload: %q", got)
	}
}

func TestToolCard_EditAnchoredMissingAnchorIsCompact(t *testing.T) {
	out := `error: edit_anchored: operation 1: anchor is required`
	got := stripANSI(renderToolCard(
		"edit_anchored",
		"edit_anchored(internal/tui/model.go, 1 ops)",
		`{"path":"internal/tui/model.go","operations":[{"op":"insert_after","new_text":"x"}]}`,
		out,
		true,
		100,
		"",
		0,
	))
	if !strings.Contains(got, "missing anchor") {
		t.Fatalf("card should classify missing anchors: %q", got)
	}
	if !strings.Contains(got, "anchors=true") {
		t.Fatalf("card should steer retry through anchored reads: %q", got)
	}
	if !strings.Contains(got, "recoverable: missing anchor") {
		t.Fatalf("card should use a recoverable footer: %q", got)
	}
	if strings.Count(got, "anchor is required") > 0 {
		t.Fatalf("card should not dump raw anchor error prose: %q", got)
	}
}

func TestToolCard_EditAnchoredStaleAnchorIsCompact(t *testing.T) {
	out := `error: edit_anchored: operation 1: stale anchor 91f36ec2 — no current line matches this anchor hash`
	got := stripANSI(renderToolCard(
		"edit_anchored",
		"edit_anchored(internal/tui/model.go, 1 ops)",
		`{"path":"internal/tui/model.go","operations":[{"op":"insert_after","anchor":"91f36ec2","new_text":"x"}]}`,
		out,
		true,
		100,
		"",
		0,
	))
	if !strings.Contains(got, "stale anchor") {
		t.Fatalf("card should classify stale anchors: %q", got)
	}
	if !strings.Contains(got, "anchors=true") || !strings.Contains(got, "line#anchor") {
		t.Fatalf("card should steer retry through current anchors: %q", got)
	}
	if !strings.Contains(got, "operation 1") || !strings.Contains(got, "91f36ec2") {
		t.Fatalf("card should retain concise anchor detail: %q", got)
	}
	if !strings.Contains(got, "recoverable: stale anchor") {
		t.Fatalf("card should use a recoverable footer: %q", got)
	}
}

// edit_file falls back to the generic text-body path when argsJSON is
// missing or malformed (e.g., a buggy adapter or a test harness emitting
// the event without args). The card must still render — never panic.
func TestRenderToolCard_EditFileFallsBackWhenArgsMissing(t *testing.T) {
	got := stripANSI(renderToolCard(
		"edit_file",
		"edit_file(main.go, single)",
		"",
		"edited /abs/main.go: 1 replacement(s)",
		false,
		80,
		"",
		0,
	))
	if !strings.HasPrefix(got, "┌ edit_file(main.go, single)") {
		t.Errorf("header should still render: %q", got)
	}
	if !strings.Contains(got, "└ edited /abs/main.go: 1 replacement(s)") {
		t.Errorf("footer should still render: %q", got)
	}
}

// edit_anchored gets a compact header with path + op count.
func TestToolHeader_EditAnchored(t *testing.T) {
	got := toolHeader("edit_anchored", `{"path":"main.go","operations":[{"op":"insert_after"},{"op":"delete_range"}]}`, "edit_anchored(main.go, 2 ops)", 120, "")
	if got != "edit_anchored(main.go, 2 ops)" {
		t.Fatalf("toolHeader(edit_anchored) = %q", got)
	}
}

// toolHeader rewrites the per-tool preview into a short verb-style
// label. The agent's PreviewCall output (e.g. "mkdir(.yottacode)") is
// only a fallback for unknown tools / empty args; with argsJSON in hand
// the TUI emits the cleaner form ("Mkdir(.yottacode)"). This locks
// the user-visible headers for the tools the user actually sees.
func TestToolHeader_RewritesPerToolPreviews(t *testing.T) {
	cases := []struct {
		tool, args, fallback, want string
	}{
		{"run_bash", `{"command":"ls -la"}`, "run_bash: ls -la", "Bash(ls -la)"},
		{"mkdir", `{"path":".yottacode"}`, "mkdir(.yottacode)", "Mkdir(.yottacode)"},
		{"write_file", `{"path":"foo.go","content":"x"}`, "write_file(foo.go, 1 bytes)", "Write(foo.go)"},
		{"read_file", `{"path":"foo.go"}`, "read_file(foo.go)", "Read(foo.go)"},
		{"read_file", `{"path":"foo.go","offset":10,"limit":20}`, "read_file(foo.go, offset=10, limit=20)", "Read(foo.go @ L10+20)"},
		{"edit_file", `{"path":"foo.go","replace_all":true}`, "edit_file(foo.go, all)", "Edit(foo.go, all)"},
		{"list_dir", `{"path":"internal"}`, "list_dir(internal)", "List(internal)"},
		{"list_dir", `{}`, "list_dir(.)", "List(.)"},
		{"grep", `{"pattern":"foo","path":"."}`, `grep("foo" in .)`, `Grep("foo")`},
		{"glob", `{"pattern":"*.go"}`, "glob(*.go in .)", "Glob(*.go)"},
		{"copy_file", `{"src":"a","dst":"b"}`, "copy_file(a -> b)", "Copy(a → b)"},
		{"move_file", `{"src":"a","dst":"b"}`, "move_file(a -> b)", "Move(a → b)"},
		{"delete_file", `{"path":"a"}`, "delete_file(a)", "Delete(a)"},
		{"fetch_url", `{"url":"https://example.com/"}`, "fetch_url(https://example.com/)", "Fetch(https://example.com/)"},
		{"lsp_status", `{"path":"."}`, "lsp_status(.)", "LSP(status .)"},
		{"lsp_symbols", `{"query":"Foo","path":"internal"}`, `lsp_symbols("Foo" in internal)`, `LSP(symbols "Foo" in internal)`},
		{"lsp_document_symbols", `{"path":"main.go"}`, "lsp_document_symbols(main.go)", "LSP(document symbols main.go)"},
		{"lsp_document_highlights", `{"path":"main.go","line":1,"character":2}`, "lsp_document_highlights(main.go:1:2)", "LSP(document highlights main.go:1:2)"},
		{"lsp_selection_ranges", `{"path":"main.go","line":1,"character":2}`, "lsp_selection_ranges(main.go:1:2)", "LSP(selection ranges main.go:1:2)"},
		{"lsp_definition", `{"path":"main.go","line":1,"character":2}`, "lsp_definition(main.go:1:2)", "LSP(definition main.go:1:2)"},
		{"lsp_references", `{"path":"main.go","line":1,"character":2}`, "lsp_references(main.go:1:2)", "LSP(references main.go:1:2)"},
		{"lsp_signature_help", `{"path":"main.go","line":1,"character":2}`, "lsp_signature_help(main.go:1:2)", "LSP(signature main.go:1:2)"},
		{"lsp_code_action_preview", `{"path":"main.go","line":1,"character":2,"index":3}`, "lsp_code_action_preview(main.go:1:2 #3)", "LSP(code action #3 main.go:1:2)"},
		{"git", `{"args":["status","-sb"]}`, "$ git status -sb", "Git(status -sb)"},
		{"git_commit", `{"message":"x"}`, `git_commit("x")`, "Git(commit)"},
		{"run_tests", `{"command":"go test ./..."}`, "run_tests(go test ./... in .)", "Test(go test ./...)"},
		{"memory_save", `{"scope":"user","name":"foo"}`, "memory_save(...)", "Memory(save user/foo)"},
		{"unknown_tool", `{"x":1}`, "unknown_tool(x=1)", "unknown_tool(x=1)"}, // fallback path
		{"run_bash", ``, "run_bash: ls", "run_bash: ls"},                      // empty argsJSON → fallback
	}
	for _, tc := range cases {
		got := toolHeader(tc.tool, tc.args, tc.fallback, 120, "")
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// run_bash's header is recomputed from argsJSON (see
// TestToolHeader_RewritesPerToolPreviews), which would otherwise silently
// swallow the "[podman]" sandbox tag RunBashTool.PreviewCall puts on
// preview — a sandboxed run_bash card would then render identically to an
// unsandboxed one. The tag must carry through.
func TestToolHeader_RunBashCarriesSandboxTag(t *testing.T) {
	got := toolHeader("run_bash", `{"command":"ls -la"}`, "[podman] run_bash: ls -la", 120, "")
	if got != "[podman] Bash(ls -la)" {
		t.Errorf("got %q, want sandbox tag preserved", got)
	}
}

func TestToolHeader_RunBashOmitsTagWhenPreviewUntagged(t *testing.T) {
	got := toolHeader("run_bash", `{"command":"ls -la"}`, "run_bash: ls -la", 120, "")
	if got != "Bash(ls -la)" {
		t.Errorf("got %q, want no tag prefix for an unsandboxed preview", got)
	}
}

// TestToolHeader_MalformedSandboxTagDegradesGracefully pins the fail-safe
// behavior documented on agent.Sandbox.Label(): a hypothetical future
// Sandbox implementation whose label doesn't follow the "[name]" bracket
// contract (see internal/agent/sandbox.go) must not crash or corrupt the
// header — the tag is just silently absent, same as an untagged preview.
func TestToolHeader_MalformedSandboxTagDegradesGracefully(t *testing.T) {
	cases := []string{
		"podman] run_bash: ls -la",  // missing leading [
		"[podman run_bash: ls -la",  // missing "] " close
		"[podman run_bash: ls -la]", // ] present but not after a name
	}
	for _, preview := range cases {
		got := toolHeader("run_bash", `{"command":"ls -la"}`, preview, 120, "")
		if got != "Bash(ls -la)" {
			t.Errorf("preview %q: got %q, want the plain untagged header (malformed tags must degrade safely)", preview, got)
		}
	}
}

func TestToolHeader_MalformedWriteFileArgsFallsBackToPreview(t *testing.T) {
	got := toolHeader("write_file", `{"path":`, "write_file(, 0 bytes)", 120, "")
	if got != "write_file(, 0 bytes)" {
		t.Errorf("malformed args should use preview fallback, got %q", got)
	}
}

// Destructive-flag git invocations get a clean single-line header. The
// "⚠ DESTRUCTIVE FLAG(S)" warning is lifted into a body row by
// renderToolCard (see TestRenderToolCard_GitDestructiveWarningInBody)
// so it gets the gutter alignment and a distinct color, instead of
// floating unaligned next to the header.
func TestToolHeader_GitHeaderIsAlwaysSingleLine(t *testing.T) {
	preview := "⚠ DESTRUCTIVE FLAG(S): --force\n  $ git push --force"
	got := toolHeader("git", `{"args":["push","--force"]}`, preview, 120, "")
	if got != "Git(push --force)" {
		t.Errorf("header should be the clean single-line form, got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("header must not contain a newline: %q", got)
	}
}

// fetch_url body strips the raw HTTP response and keeps just the
// metadata rows (Status / Content-Type / optional Note). The URL is
// dropped from the body because the card header already carries it.
// Without this, the card dumps tens of kilobytes of minified HTML.
func TestToolCard_FetchURLBodyKeepsMetadataDropsContent(t *testing.T) {
	out := "URL: https://example.com\n" +
		"Status: 200\n" +
		"Content-Type: text/html; charset=utf-8\n" +
		"Note: response truncated to 65536 bytes\n" +
		"\n" +
		"<!doctype html><html>" + strings.Repeat("x", 65000) + "</html>"

	body := toolBodyLines("fetch_url", out, false, "")
	want := []string{
		"Status: 200",
		"Content-Type: text/html; charset=utf-8",
		"Note: response truncated to 65536 bytes",
	}
	if len(body) != len(want) {
		t.Fatalf("body = %d lines, want %d: %v", len(body), len(want), body)
	}
	for i, w := range want {
		if body[i] != w {
			t.Errorf("body[%d] = %q, want %q", i, body[i], w)
		}
	}
	for _, line := range body {
		if strings.HasPrefix(line, "URL:") {
			t.Errorf("URL line should be dropped (header carries it): %q", line)
		}
		if strings.Contains(line, "doctype") || strings.Contains(line, "<html>") {
			t.Errorf("raw HTML body must not leak into the card: %q", line)
		}
	}
	footer := stripANSI(toolFooter("fetch_url", out, false, ""))
	if !strings.HasSuffix(footer, " bytes") {
		t.Errorf("footer should report byte count of the response body: %q", footer)
	}
}

// gitDestructiveWarning lifts the "⚠ DESTRUCTIVE FLAG(S): …" line out
// of the agent's preview when present, returns empty otherwise.
// renderToolCard prepends it as a styled body row for git invocations
// with dangerous flags.
func TestGitDestructiveWarning_Extraction(t *testing.T) {
	cases := []struct {
		preview, want string
	}{
		{"$ git status -sb", ""},
		{"⚠ DESTRUCTIVE FLAG(S): --force\n  $ git push --force", "⚠ DESTRUCTIVE FLAG(S): --force"},
		{"⚠ DESTRUCTIVE FLAG(S): --hard, -D\n  $ git reset --hard", "⚠ DESTRUCTIVE FLAG(S): --hard, -D"},
	}
	for _, tc := range cases {
		got := gitDestructiveWarning(tc.preview)
		if got != tc.want {
			t.Errorf("preview %q: got %q, want %q", tc.preview, got, tc.want)
		}
	}
}

// End-to-end: a destructive git invocation renders with a single-line
// `┌ Git(...)` header AND a "⚠ DESTRUCTIVE FLAG(S)" body row sitting
// under the gutter — the failure mode we're avoiding is the warning
// floating above the body without a `│` prefix.
func TestRenderToolCard_GitDestructiveWarningInBody(t *testing.T) {
	preview := "⚠ DESTRUCTIVE FLAG(S): --force\n  $ git push --force origin main"
	out := "$ git push --force origin main\nexit=0\n--- stdout ---\n--- stderr ---\nTo origin\n"
	got := stripANSI(renderToolCard(
		"git",
		preview,
		`{"args":["push","--force","origin","main"]}`,
		out,
		false,
		80,
		"",
		0,
	))
	if !strings.Contains(got, "┌ Git(push --force origin main)") {
		t.Errorf("header should be single-line `┌ Git(...)`, got: %q", got)
	}
	if !strings.Contains(got, "│ ⚠ DESTRUCTIVE FLAG(S): --force") {
		t.Errorf("warning should render as a body row with the gutter prefix, got: %q", got)
	}
	if !strings.Contains(got, "└ exit 0") {
		t.Errorf("footer should surface the exit code, got: %q", got)
	}
}

// Regression: if the agent submits an arg with an embedded newline
// (`{"path":".\n"}` was observed in the wild for list_dir), the
// header used to render across two rows and the second row had no
// `┌ ` gutter — the card's box shape collapsed. clipHeader strips
// ASCII control chars so any tool's header stays single-row.
func TestToolHeader_StripsControlCharsInArgs(t *testing.T) {
	cases := []struct {
		tool, args, want string
	}{
		{"list_dir", `{"path":".\n"}`, "List(.)"},
		{"read_file", `{"path":"foo\n.go"}`, "Read(foo.go)"},
		{"write_file", `{"path":"x\ty.txt"}`, "Write(xy.txt)"},
		{"mkdir", `{"path":".yottacode\r\n"}`, "Mkdir(.yottacode)"},
		// grep uses %q which already escapes newlines as "\n" (literal
		// backslash+n) — they're not raw control chars to begin with, so
		// the stripper has nothing to do.
		{"grep", `{"pattern":"a\nb"}`, `Grep("a\nb")`},
	}
	for _, tc := range cases {
		got := toolHeader(tc.tool, tc.args, "fallback", 120, "")
		if got != tc.want {
			t.Errorf("%s with args %s: got %q, want %q", tc.tool, tc.args, got, tc.want)
		}
		// And under no circumstance should the result contain a newline —
		// that's the actual card-shape invariant we're protecting.
		if strings.ContainsAny(got, "\n\r\t") {
			t.Errorf("%s: header still contains a control char: %q", tc.tool, got)
		}
	}
}

// When cwd is non-empty, path-typed tool headers collapse the cwd
// prefix to "." for readability.
func TestToolHeader_CwdCollapseInPathHeaders(t *testing.T) {
	const cwd = "/home/me/proj"
	cases := []struct {
		tool, args, want string
	}{
		{"write_file", `{"path":"/home/me/proj/internal/x.go"}`, "Write(./internal/x.go)"},
		{"read_file", `{"path":"/home/me/proj/main.go"}`, "Read(./main.go)"},
		{"edit_file", `{"path":"/home/me/proj/a.go","replace_all":false}`, "Edit(./a.go, single)"},
		{"delete_file", `{"path":"/home/me/proj/tmp.txt"}`, "Delete(./tmp.txt)"},
		{"mkdir", `{"path":"/home/me/proj/.cache"}`, "Mkdir(./.cache)"},
		{"list_dir", `{"path":"/home/me/proj"}`, "List(.)"},
		{"list_project_structure", `{"path":"/home/me/proj"}`, "Tree(.)"},
		{"glob", `{"pattern":"*.go","root":"/home/me/proj/internal"}`, "Glob(*.go in ./internal)"},
		{"grep", `{"pattern":"foo","path":"/home/me/proj/internal"}`, `Grep("foo" in ./internal)`},
	}
	for _, tc := range cases {
		got := toolHeader(tc.tool, tc.args, "fallback", 120, cwd)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// run_bash headers collapse cwd inside the command text.
func TestToolHeader_RunBashCollapsesCwdInCommandText(t *testing.T) {
	const cwd = "/home/me/proj"
	args := `{"command":"cd /home/me/proj && grep -r foo internal/"}`
	got := toolHeader("run_bash", args, "fallback", 120, cwd)
	if !strings.Contains(got, "cd . &&") {
		t.Errorf("expected `cd .` in shortened header, got: %q", got)
	}
	if strings.Contains(got, "/home/me/proj") {
		t.Errorf("expected absolute cwd to be removed, got: %q", got)
	}
}

// Empty cwd disables shortening — replay/test paths that don't know
// the live working directory get the same headers as before.
func TestToolHeader_EmptyCwdDisablesShortening(t *testing.T) {
	args := `{"path":"/home/me/proj/internal/x.go"}`
	got := toolHeader("write_file", args, "fallback", 120, "")
	if got != "Write(/home/me/proj/internal/x.go)" {
		t.Errorf("expected absolute path with empty cwd, got: %q", got)
	}
}

// Sibling-of-cwd paths must NOT be partially-rewritten. `/home/me/proj`
// as cwd should leave `/home/me/proj-archive/...` alone — otherwise the
// shortening would emit nonsense like `./-archive/...`.
func TestToolHeader_DoesNotClobberCwdSiblings(t *testing.T) {
	const cwd = "/home/me/proj"
	args := `{"path":"/home/me/proj-archive/notes.md"}`
	got := toolHeader("write_file", args, "fallback", 120, cwd)
	if got != "Write(/home/me/proj-archive/notes.md)" {
		t.Errorf("sibling path must be left intact, got: %q", got)
	}
}

// Grep body lines collapse cwd. The grep tool prints `/abs/path:line:match`
// per result; the user reads the body, so the path needs shortening too —
// not just the header.
func TestToolBodyLines_GrepCollapsesCwd(t *testing.T) {
	const cwd = "/home/me/proj"
	out := "/home/me/proj/internal/agent/foo.go:42:func Bar() {}\n/home/me/proj/internal/tui/x.go:7:func Baz() {}"
	lines := toolBodyLines("grep", out, false, cwd)
	if len(lines) != 2 {
		t.Fatalf("expected 2 body lines, got %d: %v", len(lines), lines)
	}
	want0 := "./internal/agent/foo.go:42:func Bar() {}"
	want1 := "./internal/tui/x.go:7:func Baz() {}"
	if lines[0] != want0 {
		t.Errorf("lines[0] = %q, want %q", lines[0], want0)
	}
	if lines[1] != want1 {
		t.Errorf("lines[1] = %q, want %q", lines[1], want1)
	}
}

// Edit footer collapses the absolute path the tool returns. The agent's
// Execute emits `edited /abs/path: N replacement(s)`; the footer should
// read `./relpath: N replacement(s)` so it matches the header.
func TestToolFooter_EditFileCollapsesCwd(t *testing.T) {
	const cwd = "/home/me/proj"
	out := "edited /home/me/proj/internal/x.go: 3 replacement(s)"
	footer := stripANSI(toolFooter("edit_file", out, false, cwd))
	want := "edited ./internal/x.go: 3 replacement(s)"
	if footer != want {
		t.Errorf("footer = %q, want %q", footer, want)
	}
}

// Long bash commands get clipped so the header never wraps. The clip
// tail is "…)" so the closing paren stays the visible end-of-args
// marker.
func TestToolHeader_LongCommandIsClipped(t *testing.T) {
	long := strings.Repeat("rg --files | grep foo | xargs wc -l ; ", 10)
	got := toolHeader("run_bash", `{"command":"`+long+`"}`, "run_bash: …", 40, "")
	if !strings.HasSuffix(got, "…)") {
		t.Errorf("clipped header should end with `…)`: %q", got)
	}
	if len([]rune(got)) > 40 {
		t.Errorf("clipped header should fit in 40 cols: width=%d, %q", len([]rune(got)), got)
	}
}

// gitFooter parses the git tool's "$ git X Y\nexit=N\n--- stdout ---..."
// envelope and colors the exit code green/red the same way run_bash
// does. Without this, a failed `git push` would render with the
// generic dim "done" footer and the failure would be easy to miss.
func TestGitFooter_SurfacesExitCode(t *testing.T) {
	ok := "$ git status\nexit=0\n--- stdout ---\n## main\n--- stderr ---\n"
	if got := stripANSI(toolFooter("git", ok, false, "")); got != "exit 0" {
		t.Errorf("ok footer = %q, want 'exit 0'", got)
	}
	bad := "$ git push\nexit=128\n--- stdout ---\n--- stderr ---\nfatal: nothing to push\n"
	if got := stripANSI(toolFooter("git", bad, false, "")); got != "exit 128" {
		t.Errorf("bad footer = %q, want 'exit 128'", got)
	}
}

// Long output is truncated to cardBodyLineCap with a "…N more line(s)"
// notice — the card stays scannable when a tool dumps thousands of
// lines.
func TestRenderToolCard_TruncatesLongBody(t *testing.T) {
	var entries []string
	for i := 0; i < 25; i++ {
		entries = append(entries, "f\tfile"+strings.Repeat("x", i))
	}
	out := strings.Join(entries, "\n") + "\n"
	got := stripANSI(renderToolCard("list_dir", "list_dir(.)", "", out, false, 80, "", 0))
	if !strings.Contains(got, "…15 more line(s)") {
		t.Errorf("card should signal truncation past cardBodyLineCap: %q", got)
	}
	// Listing output keeps the HEAD: the first entry survives, the
	// last is elided.
	if !strings.Contains(got, "file\n") && !strings.Contains(got, "file ") {
		t.Errorf("head-truncated card should keep the first entry: %q", got)
	}
	if strings.Contains(got, "file"+strings.Repeat("x", 24)) {
		t.Errorf("head-truncated card should elide the last entry: %q", got)
	}
}

// Command-envelope tools (run_bash, run_tests, git) keep the TAIL of
// an overflowing body — the test summary / final error lives in the
// last lines, and showing lines 1-10 of a 200-line test run hid the
// verdict. The elision marker moves to the top of the body.
func TestRenderToolCard_RunBashKeepsTailOnOverflow(t *testing.T) {
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, fmt.Sprintf("step %d", i))
	}
	lines = append(lines, "FAIL: TestSomething (0.03s)")
	out := "exit=1\n--- stdout ---\n" + strings.Join(lines, "\n") + "\n--- stderr ---\n"
	got := stripANSI(renderToolCard("run_bash", "run_bash: go test ./...", "", out, false, 80, "", 0))

	if !strings.Contains(got, "…16 earlier line(s)") {
		t.Errorf("tail-truncated card should elide the head with an 'earlier' marker: %q", got)
	}
	if !strings.Contains(got, "FAIL: TestSomething") {
		t.Errorf("tail-truncated card must keep the final verdict line: %q", got)
	}
	if strings.Contains(got, "step 1\n") || strings.Contains(got, "│ step 1 ") {
		t.Errorf("tail-truncated card should drop the earliest lines: %q", got)
	}
	// Marker sits above the surviving body lines, not below.
	markerIdx := strings.Index(got, "earlier line(s)")
	verdictIdx := strings.Index(got, "FAIL: TestSomething")
	if markerIdx == -1 || verdictIdx == -1 || markerIdx > verdictIdx {
		t.Errorf("elision marker should precede the kept tail: %q", got)
	}
}

// A failed tool call tints the whole ┌ │ └ frame Error red so a bad card
// is findable at a glance while scanning back. Forced color profile —
// under `go test` lipgloss renders plain ASCII, which would make the
// tinted and neutral gutters indistinguishable.
// An errored card's gutter differs from a clean one in BOTH color and
// shape: single-line ┌│└ (light box) switches to double-line ╔║╚, so the
// error signal survives NO_COLOR and colorblindness, not just red-vs-dim.
func TestRenderToolCard_ErroredGutterTintsFrameRed(t *testing.T) {
	prevProfile := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	compat.HasDarkBackground = true
	t.Cleanup(func() { lipgloss.Writer.Profile = prevProfile })

	got := renderToolCard("run_bash", "run_bash: ./x", `{"command":"./x"}`,
		"exit=1\n--- stdout ---\n--- stderr ---\nboom\n", true, 80, "", 0)

	for _, glyph := range []string{"╔ ", "║ ", "╚ "} {
		if want := styleCardErrGutter.Render(glyph); !strings.Contains(got, want) {
			t.Errorf("errored card should tint double-line %q Error-red; not found in:\n%q", glyph, got)
		}
	}
	if strings.Contains(got, styleCardGutter.Render("└ ")) {
		t.Errorf("errored card's corner must be Error-red, not neutral:\n%q", got)
	}
	for _, glyph := range []string{"┌ ", "│ ", "└ "} {
		if strings.Contains(got, glyph) {
			t.Errorf("errored card must not fall back to the single-line %q gutter:\n%q", glyph, got)
		}
	}
}

// A clean tool call keeps the whole frame neutral Dim, single-line box —
// no state color, no double-line shape. (The closing └ used to tint
// Success green, but a green corner on nearly every card was too much
// green; the red+doubled error frame carries the signal instead.)
func TestRenderToolCard_SuccessGutterIsNeutral(t *testing.T) {
	prevProfile := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	compat.HasDarkBackground = true
	t.Cleanup(func() { lipgloss.Writer.Profile = prevProfile })

	got := renderToolCard("list_dir", "list_dir(.)", "", "d\tbin\nf\tmain.go\n", false, 80, "", 0)

	for _, glyph := range []string{"┌ ", "│ ", "└ "} {
		if want := styleCardGutter.Render(glyph); !strings.Contains(got, want) {
			t.Errorf("clean card should keep %q neutral Dim; not found in:\n%q", glyph, got)
		}
	}
	if strings.Contains(got, styleCardErrGutter.Render("└ ")) {
		t.Errorf("clean card must not use the Error-red corner:\n%q", got)
	}
	for _, glyph := range []string{"╔ ", "║ ", "╚ "} {
		if strings.Contains(got, glyph) {
			t.Errorf("clean card must not use the double-line error gutter %q:\n%q", glyph, got)
		}
	}
}

// A slow call (≥ slowCallThreshold) surfaces a right-aligned duration tag
// in the header as compact metadata next to the invocation. Keeping the tag
// inline avoids huge whitespace gaps on wide terminals.
func TestRenderToolCard_SlowCallShowsDurationTag(t *testing.T) {
	got := stripANSI(renderToolCard("run_bash", "run_bash: go build", `{"command":"go build"}`,
		"exit=0\n--- stdout ---\n--- stderr ---\n", false, 80, "", 4*time.Second))
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(header, "4s") {
		t.Errorf("slow call (4s ≥ threshold) should show a duration tag; header = %q", header)
	}
	if !strings.Contains(header, "Bash(go build) · 4s") {
		t.Errorf("duration tag should be compact inline metadata; header = %q", header)
	}
	if strings.Contains(header, "     4s") {
		t.Errorf("duration tag should not be padded to the far edge; header = %q", header)
	}
}

// A sub-second call (the common case) renders no duration tag — the
// header ends with the invocation's closing paren, no timing noise.
func TestRenderToolCard_FastCallHidesDurationTag(t *testing.T) {
	got := stripANSI(renderToolCard("run_bash", "run_bash: echo hi", `{"command":"echo hi"}`,
		"exit=0\n--- stdout ---\nhi\n--- stderr ---\n", false, 80, "", 200*time.Millisecond))
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasSuffix(strings.TrimRight(header, " "), ")") {
		t.Errorf("sub-second call header should end with the invocation, no tag; header = %q", header)
	}
}

// slowDurationTag is silent below the threshold and speaks formatDuration's
// vocabulary at or above it.
func TestSlowDurationTag_Threshold(t *testing.T) {
	if got := slowDurationTag(999 * time.Millisecond); got != "" {
		t.Errorf("just under 1s should be silent; got %q", got)
	}
	if got := slowDurationTag(time.Second); got == "" {
		t.Errorf("exactly 1s should render a tag; got empty")
	}
	if got := slowDurationTag(4 * time.Second); got != "4s" {
		t.Errorf("4s tag = %q, want %q", got, "4s")
	}
}

func TestRenderToolCard_GrepCapsCustomRows(t *testing.T) {
	var rows []string
	for i := 1; i <= 101; i++ {
		rows = append(rows, fmt.Sprintf("./internal/tui/tool_card.go:%d: toolName", i))
	}
	got := stripANSI(renderToolCard(
		"grep",
		`grep("toolName" in internal/tui)`,
		`{"pattern":"toolName","path":"internal/tui","regex":false,"ignore_case":false}`,
		strings.Join(rows, "\n")+"\n",
		false,
		100,
		"",
		0,
	))
	if strings.Count(got, "tool_card.go:") != cardBodyLineCap {
		t.Fatalf("grep card should show %d match rows, got:\n%s", cardBodyLineCap, got)
	}
	if !strings.Contains(got, "…91 more match(es)") {
		t.Fatalf("grep card should show hidden-match marker, got:\n%s", got)
	}
	if !strings.Contains(got, "└ 101 matches") {
		t.Fatalf("grep footer should report total matches, got:\n%s", got)
	}
}

func TestRenderToolCard_CodeReviewContextUsesSummaryAndWarnings(t *testing.T) {
	out := strings.Join([]string{
		"## summary",
		"code_review_context(effort=medium) · feature/ui → main · working-tree diff · 2 files changed (+120/−8) · 2,092 lines",
		"",
		"## state",
		"not_found_base=false",
		"empty_repo=false",
		"diff_empty=false",
		"diff_err=false",
		"diff_capped=true",
		"",
		"## diff",
		strings.Repeat("x\n", 20),
	}, "\n")
	got := stripANSI(renderToolCard("code_review_context", "code_review_context(effort=medium)", `{"effort":"medium"}`, out, false, 120, "", 0))
	if !strings.Contains(got, "2 files changed (+120/−8) · 2,092") || !strings.Contains(got, "lines") {
		t.Fatalf("code review card should show summary digest, got:\n%s", got)
	}
	if strings.Contains(got, "not_found_base=false") {
		t.Fatalf("code review card should hide false state flags, got:\n%s", got)
	}
	if !strings.Contains(got, "⚠ diff_capped") {
		t.Fatalf("code review card should show true exception flags, got:\n%s", got)
	}
}

func TestRenderTodoCardFromTodos_ShowsSkippedItems(t *testing.T) {
	got := stripANSI(renderTodoCardFromTodos([]agent.Todo{
		{Content: "Check git state", Status: agent.TodoCompleted},
		{Content: "Open the PR", Status: agent.TodoSkipped},
	}, 100))
	if !strings.Contains(got, "✗ Open the PR") {
		t.Fatalf("todo card should show skipped item with x icon, got:\n%s", got)
	}
	if !strings.Contains(got, "plan abandoned: Open the PR") {
		t.Fatalf("todo card should surface abandoned-plan footer, got:\n%s", got)
	}
}
