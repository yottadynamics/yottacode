package agent

import (
	"encoding/json"
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
// flip from /auto, Shift+Tab, or the plan-card [Y] hotkey takes
// effect on the next iteration with no reconstruction. atomic.Bool
// keeps that benign race detector-clean.
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
// launch with --dangerously-skip-permissions; that's the user-explicit
// "yolo" path and is intentionally session-wide so it can't be toggled
// away in the middle of a run (mirroring Claude Code).
func IsAutoModeSafetyFloor(toolName string) bool {
	switch toolName {
	case "run_bash", "git_commit", "git_checkpoint", "rollback":
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
func IsAutoModeSafeBash(argsJSON string) bool {
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
	for _, s := range segs {
		if s.Risk != RiskNone {
			return false
		}
		first := strings.SplitN(strings.TrimSpace(s.Text), " ", 2)[0]
		if !autoModeSafeBashVerbs[first] {
			return false
		}
	}
	return true
}
