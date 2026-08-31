package permissions

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LintWarnings returns human-readable warnings for permission rules that are
// valid but risky, stale, or redundant. It is intentionally advisory: the
// permission engine still honors the user's rules exactly as written, while the
// TUI can surface these notes in /permissions so broad local grants do not age
// into invisible footguns.
func (p *Permissions) LintWarnings() []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	var warnings []string
	for _, r := range append(append([]Rule{}, p.allow...), append(p.ask, p.deny...)...) {
		if !KnownPermName(r.Tool) {
			warnings = append(warnings, fmt.Sprintf("%s: unknown rule prefix %s in %s", formatRule(r), r.Tool, r.Source))
		}
	}
	for _, r := range p.allow {
		if msg := riskyAllowWarning(r, p.cwd); msg != "" {
			warnings = append(warnings, msg)
		}
		for _, d := range p.deny {
			if ruleShadows(d, r, p.cwd) {
				warnings = append(warnings, fmt.Sprintf("%s in %s is shadowed by deny rule %s in %s", formatRule(r), r.Source, formatRule(d), d.Source))
				break
			}
		}
	}
	return warnings
}

var knownPermNames = map[string]struct{}{
	"Bash": {}, "Read": {}, "Write": {}, "Edit": {}, "Mkdir": {}, "Copy": {}, "Move": {}, "Delete": {},
	"List": {}, "Glob": {}, "Grep": {}, "Fetch": {}, "Git": {}, "Github": {}, "Memory": {}, "Tests": {},
	"Rollback": {}, "MCP": {}, "Media": {}, "Document": {},
}

// KnownPermName reports whether name is a permission rule namespace that the
// tool-target registry can emit. Keeping this exported from the permissions
// package gives linting, docs tests, and future UX one source of truth instead
// of each feature carrying its own stale prefix list.
func KnownPermName(name string) bool {
	_, ok := knownPermNames[name]
	return ok
}

func riskyAllowWarning(r Rule, cwd string) string {
	rule := formatRule(r)
	switch r.Tool {
	case "Bash":
		if riskyBashAllow(r.Pattern) {
			return fmt.Sprintf("%s in %s is broad shell access; prefer typed tools or narrower verbs", rule, r.Source)
		}
	case "Git":
		if r.Pattern == "-C *" || strings.HasPrefix(r.Pattern, "-C ") {
			return fmt.Sprintf("%s in %s allows git with an alternate working tree; prefer scoped git subcommands", rule, r.Source)
		}
	case "Github", "MCP", "Memory":
		if r.Pattern == "*" {
			return fmt.Sprintf("%s in %s allows an entire integration namespace; prefer read_* or verb-specific rules", rule, r.Source)
		}
	case "Delete":
		if allowsRepoWideDelete(r.Pattern, cwd) {
			return fmt.Sprintf("%s in %s allows deleting anywhere in the repo; keep delete rules narrow", rule, r.Source)
		}
	}
	return ""
}

func ruleShadows(deny, allow Rule, cwd string) bool {
	if deny.Tool != allow.Tool {
		return false
	}
	if deny.Pattern == allow.Pattern || deny.Pattern == "*" {
		return true
	}
	if deny.Tool == "Delete" && allowsRepoWideDelete(deny.Pattern, cwd) {
		return true
	}
	if strings.HasSuffix(deny.Pattern, "*") {
		prefix := strings.TrimSuffix(deny.Pattern, "*")
		return strings.HasPrefix(allow.Pattern, prefix)
	}
	return false
}

func riskyBashAllow(pattern string) bool {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		return false
	}
	verb := fields[0]
	if verb == "env" {
		return true
	}
	if strings.HasPrefix(verb, "python") || strings.HasPrefix(verb, "node") || strings.HasPrefix(verb, "ruby") || strings.HasPrefix(verb, "perl") {
		return true
	}
	switch verb {
	case "gh", "sed", "sh", "bash", "zsh", "fish", "curl", "wget", "sudo", "chmod", "chown", "rm", "dd", "docker", "podman", "ssh", "scp", "rsync":
		return true
	default:
		return false
	}
}

func allowsRepoWideDelete(pattern, cwd string) bool {
	if cwd == "" {
		return pattern == "**" || pattern == "**/*" || pattern == "*"
	}
	cwdSlash := filepath.ToSlash(filepath.Clean(cwd))
	pattern = filepath.ToSlash(filepath.Clean(pattern))
	return pattern == cwdSlash+"/**" || pattern == cwdSlash+"/**/*" || pattern == "**" || pattern == "**/*" || pattern == "*"
}

func formatRule(r Rule) string {
	return r.Tool + "(" + r.Pattern + ")"
}
