package tui

import (
	"fmt"
	"strings"
	"testing"
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

func TestToolCard_RunBashErrorExitColored(t *testing.T) {
	// run_bash always emits the format
	// "exit=N\n--- stdout ---\n<STDOUT>\n--- stderr ---\n<STDERR>".
	// With empty stdout there's still one blank line between the two
	// section markers — the `\n` after the empty stdout body.
	out := "exit=2\n--- stdout ---\n\n--- stderr ---\nboom\n"
	body := toolBodyLines("run_bash", out, false, "")
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
func TestToolCard_ReadFileBodyIsEmpty(t *testing.T) {
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

// Errored output bypasses per-tool body shapers and renders raw — the
// user needs to see the message verbatim. Footer carries an ✗
// summary.
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

// renderToolCard composes the full card. The header gutter (╭),
// per-line body gutter (│), and footer gutter (╰) all need to be
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
	))
	if !strings.HasPrefix(got, "╭ list_dir(.)") {
		t.Errorf("card should open with `╭ <preview>`: %q", got)
	}
	if !strings.Contains(got, "│ bin/") || !strings.Contains(got, "│ main.go") {
		t.Errorf("card body should carry list_dir entries: %q", got)
	}
	if !strings.Contains(got, "╰ 2 entries") {
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
	))
	// Header: single line with the invocation, no embedded `-`/`+` rows.
	// toolHeader rewrites the raw preview into "Edit(path, scope)" once
	// argsJSON is available; the diff still goes in the body.
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasPrefix(header, "╭ Edit(main.go, single)") {
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
	if !strings.Contains(got, "╰ edited /abs/main.go: 1 replacement(s)") {
		t.Errorf("footer should carry the edit result: %q", got)
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
	))
	if !strings.HasPrefix(got, "╭ edit_file(main.go, single)") {
		t.Errorf("header should still render: %q", got)
	}
	if !strings.Contains(got, "╰ edited /abs/main.go: 1 replacement(s)") {
		t.Errorf("footer should still render: %q", got)
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
// `╭ Git(...)` header AND a "⚠ DESTRUCTIVE FLAG(S)" body row sitting
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
	))
	if !strings.Contains(got, "╭ Git(push --force origin main)") {
		t.Errorf("header should be single-line `╭ Git(...)`, got: %q", got)
	}
	if !strings.Contains(got, "│ ⚠ DESTRUCTIVE FLAG(S): --force") {
		t.Errorf("warning should render as a body row with the gutter prefix, got: %q", got)
	}
	if !strings.Contains(got, "╰ exit 0") {
		t.Errorf("footer should surface the exit code, got: %q", got)
	}
}

// Regression: if the agent submits an arg with an embedded newline
// (`{"path":".\n"}` was observed in the wild for list_dir), the
// header used to render across two rows and the second row had no
// `╭ ` gutter — the card's box shape collapsed. clipHeader strips
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
	got := stripANSI(renderToolCard("list_dir", "list_dir(.)", "", out, false, 80, ""))
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
	got := stripANSI(renderToolCard("run_bash", "run_bash: go test ./...", "", out, false, 80, ""))

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
