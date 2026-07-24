package permissions

import (
	"os"
	"path/filepath"
	"strings"
)

// PathNormalizer is the optional callback DeriveAllowRule applies to
// path-like inputs (cwd plus any absolute descriptor) before turning
// them into a glob pattern. Used by the agent loop to inject
// `worktree.NormalizeForRule`, which rewrites the auto-generated
// worktree-name segment of a yottacode worktree path to `*` so
// `[A]-always` clicks don't bake ephemeral names into the saved rule.
// A nil normalizer is treated as identity.
type PathNormalizer func(string) string

// DeriveAllowRule produces a sensible "always allow" pattern from a
// single tool call. Used when the user hits `a` in the approval modal.
//
// Strategy per tool family:
//
//   - Bash: take the first argv-token of the command + " *". e.g.
//     `go test ./...` → `Bash(go *)`. Compound commands (multiple
//     segments separated by ;|&&|||) and commands whose first token is
//     in a small "obviously dangerous" set return ok=false — the user
//     should write the rule by hand instead of getting a footgun-wide
//     blanket grant.
//   - Path-typed tools (Edit/Write/Mkdir/Delete/Read/List):
//     In-cwd descriptors → `Tool(<absolute-cwd>/**)`. The rule
//     documents which working directory it applies to, so a
//     permissions.local.json copied to another project doesn't
//     accidentally grant access there.
//     Out-of-cwd descriptors → `Tool(<parent-dir>/**)`. Lets a
//     single click on a write to `~/Desktop/notes.md` produce
//     `Write(/home/me/Desktop/**)`.
//     Suppressed when the parent dir is `/`, the user's $HOME
//     directly, or a known system root (`/etc`, `/usr`, `/bin`,
//     `/sbin`, `/var`, `/sys`, `/proc`, `/dev`, `/boot`, `/lib`,
//     `/lib64`, `/root`, `/srv`). Hand-write those if you really
//     want them — one-click blanket grants on system trees are
//     a footgun.
//   - Move/Copy: src and dst broadened independently with the same
//     rule, joined with " -> ".
//   - Git: first arg + " *".
//   - Tests: same as Bash — first argv-token of the test command + " *".
//   - Glob/Grep/Fetch/Rollback: not derived (too varied or too
//     high-trust to grant blanket on one click).
//
// ok=false means the modal should suppress the [a]lways-allow option
// for this call.
func DeriveAllowRule(toolName, argsJSON, cwd string, normalize PathNormalizer) (rule string, ok bool) {
	if normalize == nil {
		normalize = identityPath
	}
	cwd = normalize(cwd)
	target := targetFor(toolName, argsJSON, cwd)
	if target.PermName == "" {
		return "", false
	}
	switch target.PermName {
	case "Bash":
		return deriveBash(target.Descriptor)
	case "Git":
		first := strings.SplitN(strings.TrimSpace(target.Descriptor), " ", 2)[0]
		if first == "" {
			return "", false
		}
		return "Git(" + first + " *)", true
	case "Read", "Write", "Edit", "Mkdir", "Delete", "List":
		pat, ok := derivePathPattern(target.Descriptor, cwd, normalize)
		if !ok {
			return "", false
		}
		return target.PermName + "(" + pat + ")", true
	case "Tests":
		verb, ok := deriveCommandVerb(target.Descriptor)
		if !ok {
			return "", false
		}
		return "Tests(" + verb + " *)", true
	case "Move", "Copy":
		src, dst, ok := splitSrcDst(target.Descriptor)
		if !ok {
			return "", false
		}
		srcPat, sok := derivePathPattern(src, cwd, normalize)
		dstPat, dok := derivePathPattern(dst, cwd, normalize)
		if !sok || !dok {
			return "", false
		}
		return target.PermName + "(" + srcPat + " -> " + dstPat + ")", true
	}
	return "", false
}

// DeriveDenyRule produces a "never allow / block" pattern from a single
// tool call — the mirror of DeriveAllowRule, used when the user hits the
// "never" key in the approval modal. Scope is intentionally limited to
// Bash and Git, the calls a user most wants to block outright; other
// tools return ok=false so the modal doesn't offer a persistent block for
// them yet.
//
// Two deliberate differences from DeriveAllowRule:
//   - It does NOT suppress "dangerous" verbs — a permanent deny on `curl`
//     or `rm` is the whole point — nor compound commands.
//   - It blocks the command's leading VERB (`Bash(curl *)`), not an exact
//     line. run_bash rules are matched per-segment with any-deny
//     precedence, so a verb-level block reliably takes effect wherever
//     that verb appears in a chain (an exact `curl … | sh` string would
//     never match a single segment). The derived rule is shown in the
//     modal before the user commits, so an over-broad block is visible.
func DeriveDenyRule(toolName, argsJSON, cwd string) (rule string, ok bool) {
	target := targetFor(toolName, argsJSON, cwd)
	switch target.PermName {
	case "Bash":
		return deriveBashDeny(target.Descriptor)
	case "Git":
		first := strings.SplitN(strings.TrimSpace(target.Descriptor), " ", 2)[0]
		if first == "" {
			return "", false
		}
		return "Git(" + first + " *)", true
	}
	return "", false
}

// deriveBashDeny returns "Bash(<verb> *)" — a block on the command's
// leading verb. See DeriveDenyRule for why this is verb-level rather than
// an exact command line.
func deriveBashDeny(command string) (string, bool) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", false
	}
	return "Bash(" + fields[0] + " *)", true
}

func identityPath(s string) string { return s }

// derivePathPattern returns the always-allow glob for a single path
// descriptor. In-cwd descriptors return "<absolute-cwd>/**". Out-of-cwd
// absolute descriptors return "<parent-dir>/**" — except for system
// roots and $HOME directly, which suppress the option. Both `desc`
// (when absolute) and `cwd` are run through `normalize` first so
// worktree-name segments collapse to `*` before the glob is built.
func derivePathPattern(desc, cwd string, normalize PathNormalizer) (string, bool) {
	if cwd == "" {
		return "", false
	}
	if !strings.HasPrefix(desc, "/") {
		return filepath.ToSlash(cwd) + "/**", true
	}
	desc = normalize(desc)
	parent := filepath.ToSlash(filepath.Dir(desc))
	if parent == "" || parent == "/" {
		return "", false
	}
	if isSystemRoot(parent) {
		return "", false
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if parent == filepath.ToSlash(home) {
			return "", false
		}
	}
	return parent + "/**", true
}

// systemRootPrefixes is the small set of absolute paths we refuse to
// auto-grant blanket writes to from a single approval click. The user
// can still hand-write a rule against these paths — this list only
// gates the auto-derive surface. Cross-platform: POSIX/Linux roots
// plus macOS roots (some Linux entries like /proc, /sys are dead on
// Mac; some macOS entries like /System, /Library are dead on Linux —
// both are harmless, the alternative is OS-dispatched lists).
var systemRootPrefixes = []string{
	// POSIX / Linux
	"/etc", "/usr", "/bin", "/sbin", "/var", "/sys", "/proc", "/dev",
	"/boot", "/lib", "/lib64", "/root", "/srv",
	// macOS
	"/System", "/Library", "/Applications", "/Volumes", "/private", "/opt",
}

func isSystemRoot(absPath string) bool {
	absPath = filepath.ToSlash(filepath.Clean(absPath))
	for _, root := range systemRootPrefixes {
		if absPath == root || strings.HasPrefix(absPath, root+"/") {
			return true
		}
	}
	return false
}

// splitSrcDst parses Move/Copy descriptors of the form "src -> dst".
func splitSrcDst(desc string) (src, dst string, ok bool) {
	parts := strings.SplitN(desc, " -> ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// deriveBash returns "Bash(<verb> *)" for a single-segment command, or
// (_, false) for compound commands and for commands starting with a
// known dangerous verb. Compound detection is intentionally simple
// (we don't want to depend on the richer agent/cmdsplit classifier
// from this package and create a cycle): any unquoted occurrence of
// `&&`, `||`, `;`, `|`, `$(`, or backtick disables derivation.
func deriveBash(command string) (string, bool) {
	verb, ok := deriveCommandVerb(command)
	if !ok {
		return "", false
	}
	return "Bash(" + verb + " *)", true
}

// deriveCommandVerb extracts the first token of a shell command after
// safety checks (compound commands and dangerous verbs are rejected).
func deriveCommandVerb(command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	if isCompoundShellCommand(command) {
		return "", false
	}
	first := strings.SplitN(command, " ", 2)[0]
	if first == "" {
		return "", false
	}
	if dangerousBashVerbs[first] {
		return "", false
	}
	return first, true
}

// isCompoundShellCommand returns true when the command contains an
// unquoted shell separator or substitution. Quote-aware so values like
// `git commit -m "fix; the bug"` are still single-segment.
func isCompoundShellCommand(s string) bool {
	var (
		inSingle, inDouble bool
		i                  int
	)
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i += 2
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case !inSingle && !inDouble:
			switch {
			case c == ';' || c == '|' || c == '&':
				return true
			case c == '`':
				return true
			case c == '$' && i+1 < len(s) && s[i+1] == '(':
				return true
			}
		}
		i++
	}
	return false
}

// dangerousBashVerbs is a tiny list of commands where a "<verb> *"
// blanket-allow would be a footgun. The user can still write the rule
// by hand for power-use cases.
var dangerousBashVerbs = map[string]bool{
	"rm":    true,
	"dd":    true,
	"mkfs":  true,
	"sudo":  true,
	"chmod": true,
	"chown": true,
	"eval":  true,
	"curl":  true,
	"wget":  true,
}
