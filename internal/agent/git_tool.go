package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	maxGitStdout = 1 << 20 // 1 MiB
	maxGitStderr = 64 * 1024
)

// gitReadOnlyFirstArg is the unconditionally-read-only tier: these
// subcommands never mutate regardless of flags (modulo the `--output`
// guard below). The ambiguous subcommands (`branch`, `tag`, `stash`,
// `remote`, `reflog`) are classified flag-aware in gitReadOnlyInvocation
// instead of being blanket-included or blanket-prompted.
var gitReadOnlyFirstArg = map[string]bool{
	"status":      true,
	"diff":        true,
	"log":         true,
	"show":        true,
	"blame":       true,
	"grep":        true,
	"ls-files":    true,
	"ls-tree":     true,
	"cat-file":    true,
	"rev-parse":   true,
	"merge-base":  true,
	"name-rev":    true,
	"describe":    true,
	"shortlog":    true,
	"whatchanged": true,
	"version":     true,
	"help":        true,
}

// gitDestructiveFlags is best-effort highlighting in the approval preview.
// Real per-flag policy belongs to a future per-tool allowance redesign;
// for now we just make the user *see* dangerous flags. The stronger
// per-invocation classification lives in gitHighRiskReason.
var gitDestructiveFlags = map[string]bool{
	"--force":            true,
	"-f":                 true,
	"--force-with-lease": true,
	"--hard":             true,
	"-D":                 true,
	"-fd":                true,
	"-fdx":               true,
	"--prune":            true,
	"--delete":           true,
}

// gitHasOutputFlag catches `--output[=<file>]`, which turns otherwise
// read-only subcommands (diff, log, stash list/show, …) into a file
// write in the working tree. Those invocations fall back to approval.
func gitHasOutputFlag(rest []string) bool {
	for _, a := range rest {
		if a == "--output" || strings.HasPrefix(a, "--output=") {
			return true
		}
	}
	return false
}

// gitReadOnlyInvocation decides the auto-execute path. Two tiers: the
// unconditional first-arg set above, and flag-aware classification for
// the subcommands that are listings in their read form but mutations in
// others. Unknown flags fall through to approval — the policy is
// allowlist-shaped, never "deny known-bad".
func gitReadOnlyInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	// Global pre-subcommand flags (-c key=val, -C dir, --git-dir,
	// --exec-path, …) can reconfigure git underneath the subcommand
	// check; always ask.
	if strings.HasPrefix(args[0], "-") {
		return false
	}
	sub, rest := args[0], args[1:]
	if gitReadOnlyFirstArg[sub] {
		return !gitHasOutputFlag(rest)
	}
	switch sub {
	case "branch":
		return gitBranchListOnly(rest)
	case "tag":
		return gitTagListOnly(rest)
	case "remote":
		return gitRemoteReadOnly(rest)
	case "stash":
		return gitStashReadOnly(rest)
	case "reflog":
		// Bare `reflog` and `reflog show` are reads; `reflog expire` /
		// `reflog delete` destroy recovery state. (reflog used to sit in
		// the first-arg set, which silently auto-ran the destructive
		// subactions too.)
		return (len(rest) == 0 || rest[0] == "show") && !gitHasOutputFlag(rest)
	}
	return false
}

// gitBranchListOnly: listing invocations of `git branch` auto-execute.
// A free (non-flag) argument means branch CREATION unless an explicit
// list-mode flag is present, in which case free args are patterns/refs.
func gitBranchListOnly(rest []string) bool {
	listMode := false
	sawFree := false
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			sawFree = true
			continue
		}
		flag := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			flag = a[:i]
		}
		switch flag {
		case "--list", "-l", "--show-current", "--contains", "--no-contains",
			"--points-at", "--merged", "--no-merged":
			listMode = true
		case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose",
			"--sort", "--format", "--column", "--no-column", "--abbrev", "--no-abbrev":
			// display modifiers — fine, but don't prove list mode
		default:
			return false // -d, -D, -m, -f, --set-upstream-to, anything unknown
		}
	}
	return listMode || !sawFree
}

// gitTagListOnly mirrors gitBranchListOnly for `git tag`: bare and
// list-mode invocations read; a free arg without a list flag is tag
// creation.
func gitTagListOnly(rest []string) bool {
	listMode := false
	sawFree := false
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			sawFree = true
			continue
		}
		flag := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			flag = a[:i]
		}
		if isTagAnnotationCount(flag) {
			listMode = true // -n[<num>] implies list mode per git-tag(1)
			continue
		}
		switch flag {
		case "--list", "-l", "--contains", "--no-contains", "--points-at",
			"--merged", "--no-merged":
			listMode = true
		case "--sort", "--format", "--column", "--no-column", "-i", "--ignore-case":
		default:
			return false // -d, -f, -a/-s/-m (creation/signing), anything unknown
		}
	}
	return listMode || !sawFree
}

// isTagAnnotationCount matches -n, -n1, -n99 — `git tag`'s
// annotation-lines flag, which implies list mode.
func isTagAnnotationCount(flag string) bool {
	if !strings.HasPrefix(flag, "-n") || strings.HasPrefix(flag, "--") {
		return false
	}
	for _, r := range flag[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// gitRemoteReadOnly: bare `remote`, `remote -v`, and `remote get-url`
// are pure local-config reads. `remote show`/`update`/`prune` touch the
// network and the rest mutate config — all ask.
func gitRemoteReadOnly(rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	switch rest[0] {
	case "-v", "--verbose":
		return len(rest) == 1
	case "get-url":
		for _, a := range rest[1:] {
			if strings.HasPrefix(a, "-") && a != "--push" && a != "--all" {
				return false
			}
		}
		return true
	}
	return false
}

// gitStashReadOnly: only `stash list` and `stash show` read. Bare
// `git stash` is a PUSH (mutation), as are pop/apply/drop/clear/etc.
func gitStashReadOnly(rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	if rest[0] != "list" && rest[0] != "show" {
		return false
	}
	return !gitHasOutputFlag(rest[1:])
}

// gitHighRiskReason classifies history-rewriting or working-tree-
// destructive invocations, returning a short reason for the approval
// modal's strong warning copy — so a `reset --hard` cannot be approved
// with the same reflex as an `add`. Ordinary mutations return "".
func gitHighRiskReason(args []string) string {
	if len(args) == 0 {
		return ""
	}
	sub, rest := args[0], args[1:]
	has := func(want ...string) bool {
		for _, a := range rest {
			for _, w := range want {
				if a == w {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "reset":
		if has("--hard") {
			return "discards every uncommitted change (reset --hard)"
		}
	case "clean":
		for _, a := range rest {
			if a == "--force" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'f')) {
				return "permanently deletes untracked files (clean -f)"
			}
		}
	case "rebase":
		return "rewrites commit history (rebase)"
	case "filter-branch":
		return "rewrites commit history (filter-branch)"
	case "checkout":
		if has("-f", "--force") {
			return "discards local changes (checkout --force)"
		}
		if has("--") {
			return "overwrites working-tree files with committed versions (checkout -- <paths>)"
		}
	case "switch":
		if has("-f", "--force", "--discard-changes") {
			return "discards local changes (switch --discard-changes)"
		}
	case "restore":
		if !has("--staged", "-S") || has("--worktree", "-W") {
			return "overwrites uncommitted file contents (restore)"
		}
	case "branch":
		if has("-D") || (has("--delete") && has("--force", "-f")) {
			return "force-deletes a branch, including unmerged commits (-D)"
		}
	case "tag":
		if has("-d", "--delete") {
			return "deletes tag(s)"
		}
	case "push":
		if has("--force", "-f") {
			return "force-push rewrites remote history"
		}
		if has("--force-with-lease") {
			return "force-push (with lease) rewrites remote history"
		}
		if has("--delete", "-d") {
			return "deletes a remote ref"
		}
		for _, a := range rest {
			if strings.HasPrefix(a, ":") && len(a) > 1 {
				return "deletes a remote ref (push :" + a[1:] + ")"
			}
		}
	case "reflog":
		if has("expire") || has("delete") {
			return "destroys reflog recovery entries"
		}
	}
	return ""
}

// GitTool is the unified entrypoint for every git command. The model passes
// argv-style tokens (no shell), and approval policy is decided by inspecting
// the first arg.
type GitTool struct {
	Cwd *CwdRef
}

func (t *GitTool) Name() string { return "git" }

func (t *GitTool) Description() string {
	return "Run a git command. Pass arguments as a list of strings " +
		"(e.g. args=[\"status\"] or args=[\"commit\",\"-m\",\"fix bug\"]). " +
		"Read-only operations (status, diff, log, show, blame, grep, ls-files, " +
		"rev-parse, etc.) execute without approval. Mutating or network ops " +
		"(commit, push, pull, branch, checkout, merge, rebase, etc.) prompt " +
		"the user. Output is stdout + stderr + exit code; stdout capped at 1 MiB."
}

func (t *GitTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments to pass to git (each token a separate string, no shell quoting)",
			},
		},
		"required": []string{"args"},
	}
}

func (t *GitTool) RequiresApproval(argsJSON string) bool {
	args, err := parseGitArgs(argsJSON)
	if err != nil || len(args) == 0 {
		return true // be safe on malformed input
	}
	return !gitReadOnlyInvocation(args)
}

func (t *GitTool) PreviewCall(argsJSON string) string {
	args, err := parseGitArgs(argsJSON)
	if err != nil {
		return fmt.Sprintf("git (invalid args: %v)", err)
	}
	cmd := "git " + strings.Join(args, " ")
	// High-risk classification first: a specific "what this destroys"
	// line beats a bare flag list. Fall back to flag highlighting for
	// dangerous flags outside the classified set.
	if reason := gitHighRiskReason(args); reason != "" {
		return fmt.Sprintf("⚠ HIGH RISK: %s\n  $ %s", reason, cmd)
	}
	var dangerous []string
	for _, a := range args {
		if gitDestructiveFlags[a] {
			dangerous = append(dangerous, a)
		}
	}
	if len(dangerous) > 0 {
		return fmt.Sprintf("⚠ DESTRUCTIVE FLAG(S): %s\n  $ %s", strings.Join(dangerous, ", "), cmd)
	}
	return "$ " + cmd
}

func (t *GitTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	args, err := parseGitArgs(argsJSON)
	if err != nil {
		return "", fmt.Errorf("git: invalid args: %w", err)
	}
	if len(args) == 0 {
		return "", errors.New("git: args is required and must not be empty")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git: git binary not found in PATH")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = t.Cwd.Get()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: maxGitStdout}
	cmd.Stderr = &capped{buf: &stderr, max: maxGitStderr}
	runErr := cmd.Run()

	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}

	// ExitError is expected for non-zero exits — surface as data, not failure.
	// Anything else (binary missing, ctx canceled, fork failure) bubbles up.
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return "", fmt.Errorf("git: %w", runErr)
	}

	return fmt.Sprintf("$ git %s\nexit=%d\n--- stdout ---\n%s--- stderr ---\n%s",
		strings.Join(args, " "),
		exit,
		stdout.String(),
		stderr.String(),
	), nil
}

func parseGitArgs(argsJSON string) ([]string, error) {
	var a struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil, err
	}
	return a.Args, nil
}
