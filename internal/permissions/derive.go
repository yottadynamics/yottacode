package permissions

import (
	"os"
	"path/filepath"
	"strings"
)

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
//       In-cwd descriptors → `Tool(<absolute-cwd>/**)`. The rule
//         documents which working directory it applies to, so a
//         permissions.local.json copied to another project doesn't
//         accidentally grant access there.
//       Out-of-cwd descriptors → `Tool(<parent-dir>/**)`. Lets a
//         single click on a write to `~/Desktop/notes.md` produce
//         `Write(/home/me/Desktop/**)`.
//       Suppressed when the parent dir is `/`, the user's $HOME
//         directly, or a known system root (`/etc`, `/usr`, `/bin`,
//         `/sbin`, `/var`, `/sys`, `/proc`, `/dev`, `/boot`, `/lib`,
//         `/lib64`, `/root`, `/srv`). Hand-write those if you really
//         want them — one-click blanket grants on system trees are
//         a footgun.
//   - Move/Copy: src and dst broadened independently with the same
//     rule, joined with " -> ".
//   - Git: first arg + " *".
//   - Glob/Grep/Fetch/Tests/Rollback: not derived (too varied or too
//     high-trust to grant blanket on one click).
//
// ok=false means the modal should suppress the [a]lways-allow option
// for this call.
func DeriveAllowRule(toolName, argsJSON, cwd string) (rule string, ok bool) {
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
		pat, ok := derivePathPattern(target.Descriptor, cwd)
		if !ok {
			return "", false
		}
		return target.PermName + "(" + pat + ")", true
	case "Move", "Copy":
		src, dst, ok := splitSrcDst(target.Descriptor)
		if !ok {
			return "", false
		}
		srcPat, sok := derivePathPattern(src, cwd)
		dstPat, dok := derivePathPattern(dst, cwd)
		if !sok || !dok {
			return "", false
		}
		return target.PermName + "(" + srcPat + " -> " + dstPat + ")", true
	}
	return "", false
}

// derivePathPattern returns the always-allow glob for a single path
// descriptor. In-cwd descriptors return "<absolute-cwd>/**". Out-of-cwd
// absolute descriptors return "<parent-dir>/**" — except for system
// roots and $HOME directly, which suppress the option.
func derivePathPattern(desc, cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	if !strings.HasPrefix(desc, "/") {
		return filepath.ToSlash(cwd) + "/**", true
	}
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
	return "Bash(" + first + " *)", true
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
