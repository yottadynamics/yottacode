package permissions

import "strings"

// ParseDiffPaths extracts the unique set of target file paths a unified
// diff would touch. Used by apply_diff to (a) populate the permissions
// descriptor so user-authored Deny/Allow rules match the diff's targets,
// and (b) drive ValidateWritePath against DefaultDenyPaths so
// yottacode-managed state can't be patched through the diff surface.
//
// Returned paths are repo-relative — the same form `git apply` would
// resolve against the work tree. The "a/" / "b/" prefixes git emits in
// `--- a/foo` / `+++ b/foo` are stripped. /dev/null entries (new-file
// source / deleted-file destination) are skipped; the paired non-null
// side still produces a path. Renames contribute both source and
// destination.
func ParseDiffPaths(diff string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		// git pads `--- ` / `+++ ` lines with a tab + timestamp on some
		// emitters; drop anything past the first tab.
		if i := strings.IndexByte(p, '\t'); i >= 0 {
			p = p[:i]
		}
		if p == "" || p == "/dev/null" {
			return
		}
		// git wraps filenames containing special chars in double quotes.
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		// Strip git-style a/ b/ prefix. Real-world relative paths can
		// also legitimately start with "a/" or "b/", but in a unified
		// diff header the prefix is conventional and inseparable from
		// the on-disk path; users who rely on a/ b/-named directories
		// are already on a path of pain with `git apply`.
		if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
			p = p[2:]
		}
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			add(line[4:])
		case strings.HasPrefix(line, "--- "):
			add(line[4:])
		case strings.HasPrefix(line, "rename to "):
			add(line[len("rename to "):])
		case strings.HasPrefix(line, "rename from "):
			add(line[len("rename from "):])
		}
	}
	return out
}
