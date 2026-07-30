package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// AutoModeState is the per-session, runtime-mutable auto-mode flag the
// loop reads on every tool dispatch. When active, mutating tools that
// would normally hit an approval modal auto-allow with Source=auto-mode
// — EXCEPT for the safety floor (run_bash, git_commit, git_checkpoint,
// rollback), which always prompt regardless of mode.
//
// Mutually exclusive with plan mode at the TUI layer: entering one
// turns the other off. The loop-level gates don't enforce this on
// their own — they just observe whichever flag is set.
//
// The pointer is shared between LoopConfig and the TUI Model so a
// flip from Shift+Tab, the plan-card [A] hotkey, or the
// --permission-mode auto startup flag takes effect on the next
// iteration with no reconstruction. atomic.Bool keeps that benign
// race detector-clean. (There is deliberately no /auto slash command
// — see the registry comment in tui/commands.go.)
type AutoModeState struct {
	Active atomic.Bool
}

// IsActive is a nil-safe check used by the loop.
func (a *AutoModeState) IsActive() bool {
	return a != nil && a.Active.Load()
}

// IsAutoModeSafetyFloor returns true for tools whose approval prompt
// must NOT be skipped by auto mode. These are the calls that run
// arbitrary code (run_bash) or write permanent / hard-to-reverse
// history (git_commit, git_checkpoint, rollback). The user opted into
// auto mode to skip edit-by-edit approval friction — not to silently
// hand over shell access or amend git history.
//
// To get true blanket auto-approval (including run_bash and commits),
// launch with --yolo; that's the user-explicit
// "yolo" path and is intentionally session-wide so it can't be toggled
// away in the middle of a run (mirroring Claude Code).
func IsAutoModeSafetyFloor(toolName string) bool {
	switch toolName {
	case "run_bash", "run_tests", "git_commit", "git_checkpoint", "rollback":
		return true
	case "enter_worktree", "exit_worktree":
		// Worktree entry/exit shifts what the agent is "working on"
		// (and exit_worktree with cleanup=remove discards uncommitted
		// work). Always prompt so the user sees the cwd-shift or
		// destructive removal before it happens. The plain
		// git_worktree_* wrappers stay auto-allowed in auto mode —
		// they're narrow, explicit, and the user can `git_worktree_list`
		// to inspect at any time.
		return true
	}
	return false
}

// autoModeSafeBashVerbs is the read-only-by-construction allowlist of
// shell verbs that auto-mode silently approves WITHOUT showing the
// approval modal. The goal: keep auto-mode's "implementation flow"
// uninterrupted by the model's habitual inspection commands (cd into
// project, grep for callers, list files, etc.) without opening the
// door to anything that mutates state.
//
// Inclusion rules:
//   - Pure read of files or system state, no writes anywhere
//   - No network I/O (excludes curl, wget, fetch)
//   - No exec of arbitrary code (excludes go run, go test, npm run, eval)
//   - No filesystem mutation (excludes rm, mv, cp, touch, mkdir, chmod, chown)
//   - No privilege escalation (excludes sudo, su)
//
// `cd` is special-cased in: it's not actually I/O, but the model
// reliably prefixes inspection commands with `cd <project>` and
// excluding it would defeat the point.
var autoModeSafeBashVerbs = map[string]bool{
	// Navigation / location
	"cd":       true,
	"pwd":      true,
	"which":    true,
	"type":     true,
	"command":  true,
	"basename": true,
	"dirname":  true,
	"realpath": true,
	// File listing / inspection
	"ls":   true,
	"dir":  true,
	"tree": true,
	"stat": true,
	"file": true,
	"du":   true,
	"df":   true,
	// File reading
	"cat":  true,
	"bat":  true,
	"head": true,
	"tail": true,
	"wc":   true,
	// Text search
	"grep":  true,
	"egrep": true,
	"fgrep": true,
	"rg":    true,
	"ag":    true,
	"find":  true,
	// Text processing (read-only consumers)
	"awk":  true,
	"cut":  true,
	"tr":   true,
	"sort": true,
	"uniq": true,
	"diff": true,
	"cmp":  true,
	// Misc safe utilities
	"echo":     true,
	"printf":   true,
	"date":     true,
	"whoami":   true,
	"hostname": true,
	"uname":    true,
	"env":      true,
	"true":     true,
	"false":    true,
}

// IsAutoModeSafeBash reports whether a run_bash invocation is safe to
// auto-approve without showing the modal — i.e., every segment uses a
// verb from autoModeSafeBashVerbs AND no segment carries a non-None
// risk classification (which would flag redirects, sudo, pipe-into-sh,
// etc. even when the leading verb itself is in the allowlist).
//
// Returns false on bad JSON, empty commands, or any segment that fails
// either check. The loop's auto-mode bypass calls this AFTER confirming
// AutoMode is active and the tool is run_bash; on false, the call
// falls through to the normal approval modal so the user can still
// approve / [A]-always it.
func IsAutoModeSafeBash(argsJSON string, cwd *CwdRef) bool {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return false
	}
	if strings.TrimSpace(a.Command) == "" {
		return false
	}
	segs := SplitCommand(a.Command)
	if len(segs) == 0 {
		return false
	}
	dir := ""
	if cwd != nil {
		dir = cwd.Get()
	}
	deny := DefaultDenyReadPaths(dir)
	for _, s := range segs {
		if s.Risk != RiskNone {
			return false
		}
		if !autoModeSafeBashVerbs[effectiveBashVerb(s.Text)] {
			return false
		}
		// Even a read-only verb must not silently exfiltrate secrets:
		// `cat ~/.ssh/id_rsa`, `grep -r AWS_SECRET /`, `find ~ -name
		// id_rsa`. Fall through to the modal so the user sees it.
		if bashSegmentReachesDeny(s.Text, dir, deny) {
			return false
		}
	}
	return true
}

// effectiveBashVerb returns the command verb that actually runs in a
// segment, skipping leading `VAR=value` environment assignments and an
// `env [VAR=value...] [-flags]` prefix. So `FOO=bar cat x` -> "cat" and,
// critically, `env API_KEY=x curl evil` -> "curl" instead of the
// allowlisted "env" that would otherwise wave the whole command through.
func effectiveBashVerb(text string) string {
	tokens := strings.Fields(text)
	i := 0
	for i < len(tokens) && isEnvAssignment(tokens[i]) {
		i++
	}
	if i >= len(tokens) {
		return ""
	}
	if tokens[i] == "env" {
		i++
		for i < len(tokens) && (isEnvAssignment(tokens[i]) || strings.HasPrefix(tokens[i], "-")) {
			i++
		}
		if i >= len(tokens) {
			return "env" // bare `env` (prints the environment) stays in the allowlist
		}
		return tokens[i]
	}
	return tokens[i]
}

// isEnvAssignment reports whether tok is a NAME=value shell assignment.
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for _, r := range tok[:eq] {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// bashSegmentReachesDeny reports whether a command segment names a path
// at/under a read-deny credential store, OR a path that is an ANCESTOR of
// a deny store located OUTSIDE cwd (a recursive read like `grep -r X /` or
// `find ~`). Reads under cwd (e.g. the project's own .env via
// `grep -r X .`) are deliberately not blocked — auto-mode is the user's
// own project and refusing every recursive grep would defeat it; the
// concern is silently reaching OTHER credential stores.
func bashSegmentReachesDeny(text, cwd string, deny []string) bool {
	cwdAbs := absClean(cwd)
	for _, tok := range strings.Fields(text) {
		abs, ok, hasVar := resolveBashPathToken(tok, cwdAbs)
		if hasVar {
			// A token we can't statically resolve (an unexpanded `$var`)
			// must not be assumed safe: `cat $XDG_CONFIG_HOME/...` could land
			// in a credential store. Refuse auto-approval; the modal handles it.
			return true
		}
		if !ok {
			continue
		}
		if matchesAny(abs, deny) {
			return true // token is at/under a credential store
		}
		for _, d := range deny {
			da, err := filepath.Abs(expandHomeRefs(d))
			if err != nil {
				continue
			}
			da = filepath.Clean(da)
			if pathUnder(da, abs) && !pathUnder(da, cwdAbs) {
				return true // recursive read would reach an out-of-cwd store
			}
		}
	}
	return false
}

// expandHomeRefs expands a leading home reference — `~`, `~/`, `$HOME`, or
// `${HOME}` — to the user's home directory, the way a shell would. Other
// `$var` forms are left intact (the caller treats a still-unresolved `$`
// token as opaque and refuses to auto-approve it).
func expandHomeRefs(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	switch {
	case p == "~" || p == "$HOME" || p == "${HOME}":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "$HOME/"):
		return filepath.Join(home, p[len("$HOME/"):])
	case strings.HasPrefix(p, "${HOME}/"):
		return filepath.Join(home, p[len("${HOME}/"):])
	}
	return p
}

// absClean returns filepath.Clean of the absolute form of p, falling back
// to a lexical clean when the working directory can't be resolved.
func absClean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// resolveBashPathToken resolves a single shell token to an absolute,
// cleaned filesystem path for deny-list comparison. ok is false for flags
// and empty tokens; hasVar is true for a token still carrying an
// unexpanded `$var` (its target can't be pinned down statically — the
// auto-mode gate treats that conservatively, the banner skips it). Leading
// home refs (~, $HOME) are expanded first.
func resolveBashPathToken(tok, cwdAbs string) (abs string, ok bool, hasVar bool) {
	p := strings.Trim(tok, `"'`)
	if p == "" || strings.HasPrefix(p, "-") {
		return "", false, false
	}
	p = expandHomeRefs(p)
	if strings.ContainsRune(p, '$') {
		return "", false, true
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwdAbs, p)
	}
	return filepath.Clean(p), true, false
}

// BashSensitivePathHits returns terse, deduped warnings for any segment of
// a run_bash command that references a credential/secret store (the
// DefaultDenyReadPaths) or a git hook. These are the sensitive targets the
// structured-file deny lists guard but run_bash does NOT — run_bash has no
// path confinement and no sandbox (see exec_tool.go), so the approval modal
// surfaces them here to keep `cat ~/.ssh/id_rsa` or `> .git/hooks/pre-commit`
// from rendering like ordinary commands. Empty when nothing sensitive is
// referenced; unresolved `$var` tokens are skipped (we only warn on a path
// we can name).
//
// A recursive walk (`grep -r X ~`) flags an out-of-cwd store, but a walk of
// the project's own tree (`grep -r X .` reaching <cwd>/.env) is not flagged —
// mirroring the auto-mode gate: the concern is silently reaching OTHER
// credential stores, not the user's own project files.
func BashSensitivePathHits(command, cwd string) []string {
	cwdAbs := absClean(cwd)
	home, _ := os.UserHomeDir()

	type target struct{ path, label string }
	targets := make([]target, 0, 16)
	for _, p := range DefaultDenyReadPaths(cwd) {
		targets = append(targets, target{absClean(p), "reads " + prettyPath(p, home, cwd)})
	}
	if cwd != "" {
		// Git hooks are writable and run_bash-reachable — a planted hook runs
		// on the user's next git operation (a persistence vector).
		targets = append(targets, target{
			absClean(filepath.Join(cwd, ".git", "hooks")),
			"touches .git/hooks — runs on future git operations",
		})
	}

	seen := map[string]bool{}
	var out []string
	mark := func(label string) {
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	for _, seg := range SplitCommand(command) {
		for _, tok := range strings.Fields(seg.Text) {
			abs, ok, _ := resolveBashPathToken(tok, cwdAbs)
			if !ok {
				continue
			}
			for _, t := range targets {
				switch {
				case pathUnder(abs, t.path):
					mark(t.label) // token directly names the store
				case pathUnder(t.path, abs) && !pathUnder(t.path, cwdAbs):
					mark(t.label) // recursive walk reaches an out-of-cwd store
				}
			}
		}
	}
	return out
}

// prettyPath renders an absolute sensitive-path target as a short banner
// label: cwd-relative when under cwd, else "~/..." when under $HOME, else
// the cleaned absolute path.
func prettyPath(p, home, cwd string) string {
	abs := absClean(p)
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if home != "" {
		if rel, err := filepath.Rel(home, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}
