package tui

import (
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
	body := toolBodyLines("list_project_structure", out, false)
	want := []string{"internal/", "go.mod", "go.sum", "main.go"}
	if len(body) != len(want) {
		t.Fatalf("body lines = %d, want %d: %v", len(body), len(want), body)
	}
	for i, w := range want {
		if body[i] != w {
			t.Errorf("body[%d] = %q, want %q", i, body[i], w)
		}
	}
	footer := stripANSI(toolFooter("list_project_structure", out, false))
	if footer != "4 entries" {
		t.Errorf("footer = %q, want '4 entries'", footer)
	}
}

// Truncation markers the tool emits as their own line are preserved
// verbatim — the user wants to know "there's more we didn't show".
func TestToolCard_ListProjectStructureKeepsTruncationMarker(t *testing.T) {
	out := "f\t91\t2026-05-03T02:09:50Z\tgo.mod\n…[truncated at 5000 entries — narrow with path or lower max_depth]\n"
	body := toolBodyLines("list_project_structure", out, false)
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
	body := toolBodyLines("list_dir", out, false)
	want := []string{"bin/", "README.md", "link"}
	if len(body) != len(want) {
		t.Fatalf("body lines = %d, want %d: %v", len(body), len(want), body)
	}
	for i, w := range want {
		if body[i] != w {
			t.Errorf("body[%d] = %q, want %q", i, body[i], w)
		}
	}
	footer := stripANSI(toolFooter("list_dir", out, false))
	if footer != "3 entries" {
		t.Errorf("footer = %q, want '3 entries'", footer)
	}
}

// run_bash output is exit=N\n--- stdout ---\n…\n--- stderr ---\n….
// Body shows stdout (and stderr, when present, after a separator);
// footer shows the exit code with a green/red tier.
func TestToolCard_RunBashSplitsStdoutStderr(t *testing.T) {
	out := "exit=0\n--- stdout ---\nhello\nworld\n--- stderr ---\n"
	body := toolBodyLines("run_bash", out, false)
	if !contains(body, "hello") || !contains(body, "world") {
		t.Errorf("body should contain stdout lines: %v", body)
	}
	footer := stripANSI(toolFooter("run_bash", out, false))
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
	body := toolBodyLines("run_bash", out, false)
	if !contains(body, "── stderr ──") {
		t.Errorf("body should label the stderr section: %v", body)
	}
	if !contains(body, "boom") {
		t.Errorf("body should include the stderr line: %v", body)
	}
	footer := stripANSI(toolFooter("run_bash", out, false))
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
	body := toolBodyLines("read_file", out, false)
	if len(body) != 0 {
		t.Errorf("read_file body should be empty, got %v", body)
	}
	footer := stripANSI(toolFooter("read_file", out, false))
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

// write_file: empty body, footer carries the full message.
func TestToolCard_WriteFileFooterCarriesAll(t *testing.T) {
	body := toolBodyLines("write_file", "wrote 70 bytes to /home/me/hello.go", false)
	if len(body) != 0 {
		t.Errorf("write_file body should be empty (footer carries all): %v", body)
	}
	footer := stripANSI(toolFooter("write_file", "wrote 70 bytes to /home/me/hello.go", false))
	if footer != "wrote 70 bytes to /home/me/hello.go" {
		t.Errorf("footer = %q", footer)
	}
}

// Errored output bypasses per-tool body shapers and renders raw — the
// user needs to see the message verbatim. Footer carries an ✗
// summary.
func TestToolCard_ErroredOutputRendersRaw(t *testing.T) {
	body := toolBodyLines("list_dir", "list_dir: open /nope: no such file or directory", true)
	if len(body) != 1 || !strings.Contains(body[0], "no such file or directory") {
		t.Errorf("errored body should render raw: %v", body)
	}
	footer := stripANSI(toolFooter("list_dir", "list_dir: open /nope: no such file or directory", true))
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
	))
	// Header: single line with the invocation, no embedded `-`/`+` rows.
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasPrefix(header, "╭ edit_file(main.go, single)") {
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
	))
	if !strings.HasPrefix(got, "╭ edit_file(main.go, single)") {
		t.Errorf("header should still render: %q", got)
	}
	if !strings.Contains(got, "╰ edited /abs/main.go: 1 replacement(s)") {
		t.Errorf("footer should still render: %q", got)
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
	got := stripANSI(renderToolCard("list_dir", "list_dir(.)", "", out, false, 80))
	if !strings.Contains(got, "…15 more line(s)") {
		t.Errorf("card should signal truncation past cardBodyLineCap: %q", got)
	}
}

