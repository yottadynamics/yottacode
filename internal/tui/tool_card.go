package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Tool-output cards: one shape for every tool call, regardless of
// kind. Header carries the invocation + duration; body carries up to
// `cardBodyLineCap` rendered lines from the tool output (with a
// trailing "…" notice when truncated); footer summarizes the result
// — entry count, bytes written, exit code, etc.
//
// The card replaces the older two-line `▸ start` / `↳ summary` shape.
// Once the user learns this shape they can scan a long conversation
// by skimming card headers.
//
//	╭ list_dir(.)                                                 9ms
//	│ bin/
//	│ pkg/
//	│ src/
//	╰ 3 entries
//
// Per-tool customizers (toolBodyLines, toolFooter) extract a
// readable body + footer summary from the raw tool output. Falls
// back to a generic "first N lines + N total lines" shape for
// unknown tools so a new tool drops in cleanly without code changes.

const (
	cardBodyLineCap   = 10  // max body lines before we truncate with "…"
	cardMaxWidthCap   = 120 // hard cap; finer cap is terminalWidth - 4
	cardMinUsefulCols = 40  // width below which we don't bother padding the duration
)

// renderToolCard renders a complete tool-call card from the tool's
// invocation preview, output, and error state. termWidth is the
// current terminal width (m.width) — the card auto-fits.
//
// argsJSON is the raw tool-call arguments (forwarded from
// agent.ToolStart). edit_file uses it to render a proper old/new diff
// body; other tools ignore it and fall back to the standard text-body
// shape.
//
// cwd is the session working directory — path-typed tool headers,
// grep/glob body lines, and footers that bake in absolute paths all
// collapse the cwd prefix to `.` for readability. Pass "" to disable
// shortening (test fixtures, replays without a known cwd).
func renderToolCard(toolName, preview, argsJSON, output string, errored bool, termWidth int, cwd string) string {
	width := cardMaxWidthCap
	if termWidth > 0 && termWidth-4 < width {
		width = termWidth - 4
	}
	if width < cardMinUsefulCols {
		width = cardMinUsefulCols
	}

	header := renderCardHeader(toolHeader(toolName, argsJSON, preview, width, cwd))
	footer := toolFooter(toolName, output, errored, cwd)

	out := []string{header}
	// edit_file gets a structured diff body with bg-tinted +/- lines.
	// We bypass the generic text-body path because the body lines carry
	// their own (highlighted) styling — they shouldn't be wrapped in
	// styleCardBody, which would override the syntax colors.
	if !errored && toolName == "edit_file" {
		if rows, ok := editFileDiffRows(argsJSON, width); ok {
			out = append(out, rows...)
			out = append(out, styleCardGutter.Render("╰ ")+footer)
			return strings.Join(out, "\n")
		}
	}
	// write_file mirrors edit_file's diff-body shape: the whole file
	// content rendered as `+` rows (a write is, from a diff perspective,
	// pure addition). Skips the generic body path so the highlighted
	// rows don't get wrapped in styleCardBody.
	if !errored && toolName == "write_file" {
		if rows, ok := writeFileBodyRows(argsJSON, width); ok {
			out = append(out, rows...)
			out = append(out, styleCardGutter.Render("╰ ")+footer)
			return strings.Join(out, "\n")
		}
	}
	// todo_write renders one rich row per item (status icon + content),
	// not the raw "plan updated: N items" string the model gets back as
	// the tool result. We bypass the generic body path so the per-row
	// status styling (green ✓, accent ▸, dim ·) survives the
	// styleCardBody wrap. Delegates to renderTodoCardFromTodos so the
	// in-flight live card in View() and this scrollback path render
	// the same shape from the same primitive.
	if !errored && toolName == "todo_write" {
		var a struct {
			Todos []agent.Todo `json:"todos"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) == nil {
			return renderTodoCardFromTodos(a.Todos, termWidth)
		}
	}
	// Git destructive-flag warning: lifted from the agent's multi-line
	// preview into the body so it gets a proper aligned gutter and a
	// bold-red "⚠ …" row instead of floating unaligned next to the
	// header. Rendered before the regular body so it sits directly under
	// the invocation.
	if !errored && toolName == "git" {
		if w := gitDestructiveWarning(preview); w != "" {
			out = append(out, styleCardGutter.Render("│   ")+styleCardErrFooter.Render(w))
		}
	}
	body := toolBodyLines(toolName, output, errored, cwd)
	if len(body) > cardBodyLineCap {
		visible := body[:cardBodyLineCap]
		hidden := len(body) - cardBodyLineCap
		for _, line := range visible {
			out = append(out, styleCardGutter.Render("│   ")+styleCardBody.Render(line))
		}
		out = append(out, styleCardGutter.Render("│   ")+styleCardMeta.Render(fmt.Sprintf("…%d more line(s)", hidden)))
	} else {
		for _, line := range body {
			out = append(out, styleCardGutter.Render("│   ")+styleCardBody.Render(line))
		}
	}
	out = append(out, styleCardGutter.Render("╰ ")+footer)
	return strings.Join(out, "\n")
}

// renderCardHeader composes "╭ <preview>". The trailing duration tag
// that used to right-align here was removed — fast tool calls
// dominate (sub-second), and the live thinking row already shows
// elapsed time during long ones, so the per-card timestamp was just
// chrome.
func renderCardHeader(preview string) string {
	return styleCardGutter.Render("╭ ") + styleCardHeader.Render(preview)
}

// toolBodyLines extracts the displayable body for a given tool. Returns
// a slice of plain strings (no per-line gutter applied — the caller
// adds the `│ ` prefix). Each tool gets a tailored shape:
//
//   - list_dir: drop the f/d/l marker column; append `/` to directories
//   - list_project_structure: drop marker/size/mtime; append `/` to dirs
//   - run_bash: stdout-only, with stderr appended below an "── stderr ──" hint when present
//   - read_file: empty body — the model needs the content; the user
//     just needs to see "we read this file" — the footer's line/byte
//     count is enough at-a-glance signal. Dumping the first 10 lines
//     was visual noise that pushed real progress out of view.
//   - write_file: empty body — the footer "wrote N bytes" tells the whole story
//   - edit_file / git: raw output (often a unified diff or status text)
//   - glob / grep: raw output (one match per line)
//   - default: raw output trimmed of trailing whitespace
//
// Errored output is shown as-is so the user sees the error message.
//
// cwd, when non-empty, collapses the cwd prefix to "." in body lines
// that bake in absolute paths (grep/glob results, run_bash stdout,
// errors). The shortening is applied last, after per-tool shaping, so
// each per-tool case can stay in terms of raw output.
func toolBodyLines(toolName, output string, errored bool, cwd string) []string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return nil
	}
	shortenLines := func(lines []string) []string {
		if cwd == "" {
			return lines
		}
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = shortenCwdInText(l, cwd)
		}
		return out
	}
	if errored {
		return shortenLines(strings.Split(output, "\n"))
	}
	switch toolName {
	case "list_dir":
		return listDirBody(output)
	case "list_project_structure":
		return listProjectStructureBody(output)
	case "run_bash", "run_tests":
		// run_tests emits the same exit=N/stdout/stderr envelope; share
		// the splitter so its body shape matches Bash's.
		return shortenLines(runBashBody(output))
	case "git":
		if strings.Contains(output, "\n--- stdout ---") {
			return shortenLines(runBashBody(output))
		}
		// Fall through to default for older / non-enveloped output.
	case "fetch_url":
		return fetchURLBody(output)
	case "read_file", "write_file":
		// Footer carries the relevant summary; body adds noise.
		return nil
	}
	return shortenLines(strings.Split(output, "\n"))
}

// toolFooter produces the styled summary line at the card's bottom-edge.
// Format depends on tool: list_dir = "N entries", write_file =
// "wrote N bytes to path", run_bash = "exit N" (green if 0, red
// otherwise), default = a one-line truncated preview of output.
//
// cwd, when non-empty, collapses absolute paths in footers that bake
// them in (today: edit_file's "edited <abs/path>: N replacement(s)"
// line). Other footers don't carry absolute paths.
func toolFooter(toolName, output string, errored bool, cwd string) string {
	if errored {
		summary := summarizeToolOutput(output)
		return styleCardErrFooter.Render("✗ " + shortenCwdInText(summary, cwd))
	}
	switch toolName {
	case "list_dir":
		return styleCardMeta.Render(listDirFooter(output))
	case "list_project_structure":
		return styleCardMeta.Render(listDirFooter(output))
	case "run_bash":
		return runBashFooter(output)
	case "git":
		return gitFooter(output)
	case "read_file":
		return styleCardMeta.Render(readFileFooter(output))
	case "write_file":
		return styleCardMeta.Render(shortWriteFooter(output))
	case "edit_file":
		// edit_file's output is "edited <abs/path>: N replacement(s)" —
		// the whole story belongs in the footer (the diff body above
		// already carries the visual change). Collapse the absolute path
		// against cwd so the footer matches the header's shortened form.
		return styleCardMeta.Render(shortenCwdInText(strings.TrimSpace(output), cwd))
	case "todo_write":
		// Body rendered the per-item status rows; surface the model's
		// own one-liner ("plan updated: N items (M done)" / "plan
		// cleared") in the footer so the user can read the count
		// without parsing the list.
		return styleCardMeta.Render(strings.TrimSpace(output))
	case "glob":
		return styleCardMeta.Render(matchFooter(output))
	case "grep":
		return styleCardMeta.Render(matchFooter(output))
	case "fetch_url":
		return styleCardMeta.Render(fetchURLFooter(output))
	case "run_tests":
		// run_tests reuses run_bash's exit=N\n--- stdout ---\n…\n--- stderr ---\n…
		// envelope, so its footer should surface the exit code the same
		// way — green when N=0, red otherwise.
		return runBashFooter(output)
	}
	return styleCardMeta.Render("done")
}

// listDirBody parses the f/d/l\tname format the list_dir tool emits
// and renders one entry per line with a trailing slash on directories.
// Permissions, sizes, and timestamps are dropped — the model has the
// raw output; the user just needs to skim what's there.
func listDirBody(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Truncation marker the tool itself emits — keep it visible.
		if strings.HasPrefix(line, "…") {
			entries = append(entries, line)
			continue
		}
		marker, name, ok := strings.Cut(line, "\t")
		if !ok {
			entries = append(entries, line)
			continue
		}
		if marker == "d" {
			name += "/"
		}
		entries = append(entries, name)
	}
	return entries
}

// listProjectStructureBody parses the marker\tsize\tmtime\trelpath
// format the list_project_structure tool emits and renders one
// relpath per line, with a trailing slash on directories. The size
// and mtime columns are dropped for the same reason listDirBody
// drops them — the model already has the raw output; the user is
// just skimming what's there. Relpaths from depth ≥ 2 already
// contain `/` separators, so a tree listing reads as a sorted file
// inventory rather than a parsed-and-redrawn tree.
func listProjectStructureBody(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Truncation marker the tool itself emits — keep it visible.
		if strings.HasPrefix(line, "…") {
			entries = append(entries, line)
			continue
		}
		// Format: marker \t size \t mtime \t relpath. SplitN with n=4
		// keeps any literal tab inside a relpath intact (defensive —
		// regular paths don't contain tabs, but trusting the input
		// shape is cheap).
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			entries = append(entries, line)
			continue
		}
		name := parts[3]
		if parts[0] == "d" {
			name += "/"
		}
		entries = append(entries, name)
	}
	return entries
}

// readFileFooter summarizes the read in a single dim line — line count
// and byte count, with a "(truncated)" marker when the tool's output
// hit the 512 KiB read cap. The body itself is empty (see toolBodyLines)
// so this footer is the only signal the user gets that the read
// happened and how much it returned.
func readFileFooter(out string) string {
	truncated := strings.HasSuffix(out, "\n…[truncated]")
	if truncated {
		out = strings.TrimSuffix(out, "\n…[truncated]")
	}
	bytes := len(out)
	lines := strings.Count(out, "\n")
	if bytes > 0 && !strings.HasSuffix(out, "\n") {
		lines++
	}
	noun := "lines"
	if lines == 1 {
		noun = "line"
	}
	body := fmt.Sprintf("%d %s · %d bytes", lines, noun, bytes)
	if truncated {
		body += " (truncated)"
	}
	return body
}

// listDirFooter counts entries (excluding the truncation marker) and
// returns "N entries".
func listDirFooter(out string) string {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "…") {
			continue
		}
		n++
	}
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// runBashBody returns just the stdout portion of run_bash output. The
// tool emits "exit=N\n--- stdout ---\n...\n--- stderr ---\n..."; we
// strip the framing and append a "── stderr ──" hint plus the stderr
// body when present (so the user sees both streams).
func runBashBody(out string) []string {
	exit, stdout, stderr := parseRunBashOutput(out)
	_ = exit
	var lines []string
	if stdout != "" {
		lines = append(lines, strings.Split(stdout, "\n")...)
	}
	if stderr != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "── stderr ──")
		lines = append(lines, strings.Split(stderr, "\n")...)
	}
	return lines
}

// runBashFooter returns "exit N" colored Success when N=0, Error
// otherwise.
func runBashFooter(out string) string {
	exit, _, _ := parseRunBashOutput(out)
	tag := fmt.Sprintf("exit %d", exit)
	if exit == 0 {
		return styleCardOKFooter.Render(tag)
	}
	return styleCardErrFooter.Render(tag)
}

// parseRunBashOutput pulls the exit code, stdout, and stderr out of
// run_bash's combined "exit=N\n--- stdout ---\n…\n--- stderr ---\n…"
// envelope. Best-effort: if the format doesn't match, returns zero
// exit and the full output as stdout so we still render something
// useful.
//
// The separators look for the marker without requiring a trailing
// newline so an empty trailing section (e.g., empty stderr) still
// parses cleanly after the surrounding TrimRight.
func parseRunBashOutput(out string) (exit int, stdout, stderr string) {
	out = strings.TrimRight(out, "\n")
	const stdoutSep = "\n--- stdout ---"
	const stderrSep = "\n--- stderr ---"
	exitPart, rest, ok := strings.Cut(out, stdoutSep)
	if !ok {
		return 0, out, ""
	}
	// run_bash emits "exit=N\n--- stdout ---"; git emits
	// "$ git X Y\nexit=N\n--- stdout ---". Scan from the last `exit=` so
	// the leading `$ git` line doesn't trip the parser. fmt.Sscanf with a
	// %d verb stops at the first non-digit, so a trailing newline in the
	// prefix is harmless.
	if idx := strings.LastIndex(exitPart, "exit="); idx >= 0 {
		if _, err := fmt.Sscanf(exitPart[idx:], "exit=%d", &exit); err != nil {
			exit = 0
		}
	}
	stdout, stderr, _ = strings.Cut(rest, stderrSep)
	stdout = strings.TrimPrefix(stdout, "\n")
	stderr = strings.TrimPrefix(stderr, "\n")
	return exit, strings.TrimRight(stdout, "\n"), strings.TrimRight(stderr, "\n")
}

// gitFooter reuses parseRunBashOutput (which now handles the `$ git X Y\n`
// prefix the git tool prepends) and renders the exit code in green/red
// the same way run_bash does. Falls back to "done" when the envelope
// doesn't match — e.g., a future code path that returns a bare string.
func gitFooter(out string) string {
	if !strings.Contains(out, "\n--- stdout ---") {
		return styleCardMeta.Render("done")
	}
	exit, _, _ := parseRunBashOutput(out)
	tag := fmt.Sprintf("exit %d", exit)
	if exit == 0 {
		return styleCardOKFooter.Render(tag)
	}
	return styleCardErrFooter.Render(tag)
}

// shortWriteFooter trims the redundant "to <abs/path>" tail from the
// write_file tool's confirmation string, since the header already names
// the path. Input:  "wrote 70 bytes to /home/me/hello.go"
// Output: "wrote 70 bytes".
func shortWriteFooter(out string) string {
	out = strings.TrimSpace(out)
	if i := strings.Index(out, " to "); i >= 0 {
		return out[:i]
	}
	return out
}

// fetchURLBody returns just the metadata header rows from the fetch_url
// tool's output (Status, Content-Type, optional Note). The full HTTP
// response body — which can be 64+ KiB of minified HTML — is dropped:
// the model gets the full content via ToolResult, but dumping it into
// the card makes the user scroll past a wall of markup. The "URL:"
// line is also dropped because the card header already names the URL.
//
// fetch_url's output format (see internal/agent/web_tools.go):
//
//	URL: <url>\n
//	Status: <code>\n
//	Content-Type: <mime>\n
//	[Note: response truncated to N bytes\n]
//	\n
//	<raw body>
//
// — so the "metadata block" is everything before the first blank line.
func fetchURLBody(out string) []string {
	meta := out
	if i := strings.Index(out, "\n\n"); i >= 0 {
		meta = out[:i]
	}
	var rows []string
	for _, line := range strings.Split(meta, "\n") {
		if line == "" || strings.HasPrefix(line, "URL:") {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}

// fetchURLFooter returns "N bytes" — the size of the actual HTTP response
// body (everything after the first blank line that separates metadata
// from content). When the output doesn't have the expected envelope,
// falls back to the total byte count so the user still gets a signal.
func fetchURLFooter(out string) string {
	body := out
	if i := strings.Index(out, "\n\n"); i >= 0 {
		body = out[i+2:]
	}
	return fmt.Sprintf("%d bytes", len(body))
}

// matchFooter counts non-empty lines and returns "N match(es)". Used by
// glob/grep where each output line is one match. Hardcoded plural —
// "matchs" was the previous bug, and these are the only two callers.
func matchFooter(out string) string {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n == 1 {
		return "1 match"
	}
	return fmt.Sprintf("%d matches", n)
}

// toolHeader rewrites a tool's raw `preview` into a short, human-readable
// card header — verb-style label + the user-meaningful argument in
// parens. Examples:
//
//	run_bash      → Bash(rg --files | head)
//	write_file    → Write(.yottacode/YOTTACODE.md)
//	mkdir         → Mkdir(.yottacode)
//	list_dir      → List(.)
//	git           → Git(status -sb)
//
// For known tools it parses argsJSON; for unknown tools or empty
// argsJSON it returns `preview` so the card never renders blank. The
// `maxWidth` is the per-card width (terminal-minus-gutter) — long
// commands and URLs get clipped with "…" so the header never wraps.
//
// cwd, when non-empty, collapses the cwd prefix to "." in path-typed
// headers and inside run_bash command text so deep absolute paths
// don't dominate the card. Display-only; tool inputs are unchanged.
func toolHeader(toolName, argsJSON, preview string, maxWidth int, cwd string) string {
	if argsJSON == "" {
		return preview
	}
	headerBudget := maxWidth - 2 // "╭ " gutter
	if headerBudget < 20 {
		headerBudget = 20
	}
	short := func(p string) string { return displayPath(p, cwd) }
	switch toolName {
	case "run_bash":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		return clipHeader("Bash("+shortenCwdInText(oneLine(a.Command), cwd)+")", headerBudget)
	case "read_file":
		var a struct {
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Limit  int64  `json:"limit"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		if a.Offset == 0 && a.Limit == 0 {
			return clipHeader("Read("+short(a.Path)+")", headerBudget)
		}
		return clipHeader(fmt.Sprintf("Read(%s @ L%d+%d)", short(a.Path), a.Offset, a.Limit), headerBudget)
	case "write_file":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		return clipHeader("Write("+short(a.Path)+")", headerBudget)
	case "edit_file":
		var a struct {
			Path       string `json:"path"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		mode := "single"
		if a.ReplaceAll {
			mode = "all"
		}
		return clipHeader(fmt.Sprintf("Edit(%s, %s)", short(a.Path), mode), headerBudget)
	case "delete_file":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		return clipHeader("Delete("+short(a.Path)+")", headerBudget)
	case "move_file":
		var a struct {
			Src, Dst string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader("Move("+short(a.Src)+" → "+short(a.Dst)+")", headerBudget)
	case "copy_file":
		var a struct {
			Src, Dst string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader("Copy("+short(a.Src)+" → "+short(a.Dst)+")", headerBudget)
	case "mkdir":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) != nil {
			return preview
		}
		return clipHeader("Mkdir("+short(a.Path)+")", headerBudget)
	case "read_many_files":
		var a struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		noun := "files"
		if len(a.Paths) == 1 {
			noun = "file"
		}
		return clipHeader(fmt.Sprintf("Read(%d %s)", len(a.Paths), noun), headerBudget)
	case "list_dir":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.Path == "" {
			a.Path = "."
		}
		return clipHeader("List("+short(a.Path)+")", headerBudget)
	case "list_project_structure":
		var a struct {
			Path     string `json:"path"`
			MaxDepth int    `json:"max_depth"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.Path == "" {
			a.Path = "."
		}
		if a.MaxDepth > 0 {
			return clipHeader(fmt.Sprintf("Tree(%s, depth=%d)", short(a.Path), a.MaxDepth), headerBudget)
		}
		return clipHeader("Tree("+short(a.Path)+")", headerBudget)
	case "glob":
		var a struct {
			Pattern, Root string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		root := a.Root
		if root == "" || root == "." {
			return clipHeader("Glob("+a.Pattern+")", headerBudget)
		}
		return clipHeader("Glob("+a.Pattern+" in "+short(root)+")", headerBudget)
	case "grep":
		var a struct {
			Pattern, Path string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		root := a.Path
		if root == "" || root == "." {
			return clipHeader(fmt.Sprintf("Grep(%q)", a.Pattern), headerBudget)
		}
		return clipHeader(fmt.Sprintf("Grep(%q in %s)", a.Pattern, short(root)), headerBudget)
	case "fetch_url":
		var a struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader("Fetch("+a.URL+")", headerBudget)
	case "apply_diff":
		// apply_diff carries a `diff` blob — no path field. Best we can
		// do is label it as a patch op; the body shows the actual hunks.
		return clipHeader("Patch(apply)", headerBudget)
	case "git":
		// Header stays single-line `Git(args)` regardless of destructive
		// flags. The "⚠ DESTRUCTIVE FLAG(S)" warning that the agent's
		// PreviewCall emits as a multi-line prefix gets lifted into a
		// styled body row by renderToolCard — that keeps the gutter
		// aligned and gives the warning its own attention-grabbing color
		// instead of floating unaligned next to the header.
		return rewriteGitPreview(preview)
	case "git_branch_status":
		return "Git(branch status)"
	case "git_show_file_at_rev":
		var a struct{ Path, Rev string }
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.Rev == "" {
			a.Rev = "HEAD"
		}
		return clipHeader(fmt.Sprintf("Git(show %s @ %s)", a.Path, a.Rev), headerBudget)
	case "git_diff_files":
		var a struct {
			Base, Head string
			Paths      []string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		switch {
		case a.Base != "" && a.Head != "":
			return clipHeader(fmt.Sprintf("Git(diff %s..%s)", a.Base, a.Head), headerBudget)
		case a.Base != "":
			return clipHeader("Git(diff "+a.Base+")", headerBudget)
		default:
			return "Git(diff)"
		}
	case "git_stage_files":
		var a struct {
			Paths []string `json:"paths"`
			All   bool     `json:"all"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.All {
			return clipHeader("Git(stage all)", headerBudget)
		}
		return clipHeader(fmt.Sprintf("Git(stage %d %s)", len(a.Paths), pluralize("file", len(a.Paths))), headerBudget)
	case "git_create_branch":
		var a struct {
			Name       string `json:"name"`
			StartPoint string `json:"start_point"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.StartPoint != "" {
			return clipHeader(fmt.Sprintf("Git(create branch %s from %s)", a.Name, a.StartPoint), headerBudget)
		}
		return clipHeader(fmt.Sprintf("Git(create branch %s)", a.Name), headerBudget)
	case "git_unstage_files":
		var a struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader(fmt.Sprintf("Git(unstage %d %s)", len(a.Paths), pluralize("file", len(a.Paths))), headerBudget)
	case "git_commit":
		return "Git(commit)"
	case "git_log_file":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader("Git(log "+a.Path+")", headerBudget)
	case "git_blame_lines":
		var a struct {
			Path       string `json:"path"`
			Start, End int
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader(fmt.Sprintf("Git(blame %s:L%d-L%d)", a.Path, a.Start, a.End), headerBudget)
	case "git_merge_base":
		var a struct{ Base, Head string }
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader(fmt.Sprintf("Git(merge-base %s..%s)", a.Base, a.Head), headerBudget)
	case "git_checkpoint":
		var a struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if strings.TrimSpace(a.Message) == "" {
			return "Git(checkpoint)"
		}
		return clipHeader(fmt.Sprintf("Git(checkpoint %q)", a.Message), headerBudget)
	case "list_git_changed_files":
		return "Git(list changed)"
	case "rollback":
		var a struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if a.Target == "" {
			a.Target = "HEAD~1"
		}
		return clipHeader("Rollback("+a.Target+")", headerBudget)
	case "run_tests":
		var a struct {
			Command, Path string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		cmd := strings.TrimSpace(a.Command)
		if cmd == "" {
			cmd = "go test ./..."
		}
		return clipHeader("Test("+oneLine(cmd)+")", headerBudget)
	case "memory_save":
		var a struct {
			Scope, Name string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader(fmt.Sprintf("Memory(save %s/%s)", a.Scope, a.Name), headerBudget)
	case "memory_forget":
		var a struct {
			Scope, Name string
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return clipHeader(fmt.Sprintf("Memory(forget %s/%s)", a.Scope, a.Name), headerBudget)
	}
	return preview
}

// rewriteGitPreview extracts the `git X Y` portion of the agent's git
// preview and returns the single-line `Git(X Y)` header. Drops any
// "⚠ DESTRUCTIVE FLAG(S)" warning prefix the agent prepends — that
// warning is re-rendered as a body row by renderToolCard so it gets
// the aligned gutter and a distinct color instead of floating in the
// header.
//
// The git tool's preview is either
//
//	$ git status -sb                                       (safe)
//	⚠ DESTRUCTIVE FLAG(S): --force\n  $ git push --force   (destructive)
//
// — so we locate "$ git " (which appears in both shapes) and return only
// the argv that follows.
func rewriteGitPreview(preview string) string {
	idx := strings.Index(preview, "$ git ")
	if idx < 0 {
		return preview
	}
	tail := preview[idx+len("$ git "):] // everything after "$ git "
	if end := strings.IndexByte(tail, '\n'); end >= 0 {
		tail = tail[:end]
	}
	return "Git(" + tail + ")"
}

// gitDestructiveWarning returns the "⚠ DESTRUCTIVE FLAG(S): …" line from
// the agent's git preview if the agent flagged any args as dangerous —
// otherwise empty. renderToolCard uses this to insert the warning as a
// styled body row instead of letting it ride along as an unaligned
// header prefix.
func gitDestructiveWarning(preview string) string {
	if !strings.HasPrefix(preview, "⚠") {
		return ""
	}
	if i := strings.IndexByte(preview, '\n'); i >= 0 {
		return strings.TrimSpace(preview[:i])
	}
	return strings.TrimSpace(preview)
}

// clipHeader sanitizes and truncates a single-line header so it fits
// within `max` rune-width columns. ASCII control characters are
// stripped first — a stray newline in an arg (e.g., an agent sending
// {"path":".\n"}) would otherwise break the card's box shape because
// only the first row gets the `╭ ` gutter. Truncation replaces the
// tail with `…)` so the closing paren stays as the visible
// end-of-args marker.
//
// clipHeader is the choke point for every per-tool header in
// toolHeader(), so doing the sanitization here guarantees no tool can
// produce a multi-row header by accident. The destructive-flag git
// preview path bypasses clipHeader (it's intentionally multi-line) and
// is the only header that legitimately spans rows.
func clipHeader(s string, max int) string {
	s = stripControlChars(s)
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 4 {
		return string(r[:max])
	}
	// Cut to max-2 so we have room for "…)" and the closing paren stays
	// visible as the natural end-of-args marker.
	return string(r[:max-2]) + "…)"
}

// stripControlChars removes ASCII control characters (newline, CR, tab,
// etc.) from a header string. The agent can submit args with embedded
// whitespace; without this, a `\n` inside a path would split the header
// across two terminal rows and the second row would render without the
// `╭ ` gutter, collapsing the card's shape.
func stripControlChars(s string) string {
	needsClean := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			needsClean = true
			break
		}
	}
	if !needsClean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// oneLine collapses a multi-line command into a single line + "…" tail
// when the original had a newline. Used for run_bash / run_tests where
// the agent can submit a heredoc but the header has to stay one row.
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

// pluralize returns the noun with a trailing "s" unless n == 1. Used by
// git stage/unstage headers ("1 file" vs "3 files").
func pluralize(noun string, n int) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// Card-specific styles. The gutter (╭ │ ╰) renders Muted (decorative);
// the header preview is Content (the value the user cares about);
// metadata (duration, footer) renders Dim by default with state
// colors for OK / Error footers.
var (
	styleCardGutter    = lipgloss.NewStyle().Foreground(colorRule)
	styleCardHeader    = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleCardBody      = lipgloss.NewStyle().Foreground(colorContent)
	styleCardMeta      = lipgloss.NewStyle().Foreground(colorDim)
	styleCardOKFooter  = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleCardErrFooter = lipgloss.NewStyle().Foreground(colorError).Bold(true)

	// todo_write status icon + content styling — green ✓ for done
	// (strikethrough + dim content), accent ▸ for in_progress (bold
	// content), dim · for pending.
	styleTodoDone       = lipgloss.NewStyle().Foreground(colorDim).Strikethrough(true)
	styleTodoInProgress = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleTodoPending    = lipgloss.NewStyle().Foreground(colorContent)
	styleTodoCheckDone  = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleTodoArrow      = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleTodoBullet     = lipgloss.NewStyle().Foreground(colorDim)
)

func todoRow(td agent.Todo) string {
	switch td.Status {
	case agent.TodoCompleted:
		return styleTodoCheckDone.Render("✓ ") + styleTodoDone.Render(td.Content)
	case agent.TodoInProgress:
		return styleTodoArrow.Render("▸ ") + styleTodoInProgress.Render(td.Content)
	default:
		return styleTodoBullet.Render("· ") + styleTodoPending.Render(td.Content)
	}
}

// renderTodoCardFromTodos renders the complete todo_write card from a
// typed Todo slice. Used by both the scrollback path
// (renderToolCard's todo_write branch, on each ToolResult) and the
// in-flight live View() card (which updates in place every tick).
// Sharing one renderer keeps the live preview and the end-of-turn
// scrollback snapshot visually identical.
//
// The header and footer are synthesized from the slice itself — the
// header reads "Plan: N items (M done)" (matches the agent's
// PreviewCall format) and the footer reads "plan updated: N items
// (M done)" or "plan cleared" (matches what the tool's Execute
// returns to the model). That makes the helper standalone: callers
// don't have to thread argsJSON or the model's response string
// through to render correctly.
func renderTodoCardFromTodos(todos []agent.Todo, termWidth int) string {
	_ = termWidth // reserved for future row-truncation; todo content is the user's signal, never clipped today
	out := []string{renderCardHeader(todoCardHeaderText(todos))}
	if len(todos) == 0 {
		out = append(out, styleCardGutter.Render("│   ")+styleCardMeta.Render("(empty plan)"))
	} else {
		for _, td := range todos {
			out = append(out, styleCardGutter.Render("│   ")+todoRow(td))
		}
	}
	out = append(out, styleCardGutter.Render("╰ ")+styleCardMeta.Render(todoCardFooterText(todos)))
	return strings.Join(out, "\n")
}

func todoCardHeaderText(todos []agent.Todo) string {
	done := 0
	for _, td := range todos {
		if td.Status == agent.TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("Plan: %d items (%d done)", len(todos), done)
}

func todoCardFooterText(todos []agent.Todo) string {
	if len(todos) == 0 {
		return "plan cleared"
	}
	done := 0
	for _, td := range todos {
		if td.Status == agent.TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("plan updated: %d items (%d done)", len(todos), done)
}
