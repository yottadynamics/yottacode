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

// gitReadOnlyFirstArg is the auto-execute set: anything that begins with one
// of these subcommands runs without an approval prompt. Inclusion is "first
// arg only" — we don't sniff later flags. Anything ambiguous (`branch`,
// `tag`, `stash`, `remote`) is left out so the user is asked.
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
	"reflog":      true,
	"whatchanged": true,
	"version":     true,
	"help":        true,
}

// gitDestructiveFlags is best-effort highlighting in the approval preview.
// Real per-flag policy belongs to a future per-tool allowance redesign;
// for now we just make the user *see* dangerous flags.
var gitDestructiveFlags = map[string]bool{
	"--force":             true,
	"-f":                  true,
	"--force-with-lease":  true,
	"--hard":              true,
	"-D":                  true,
	"-fd":                 true,
	"-fdx":                true,
	"--prune":             true,
	"--delete":            true,
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
	return !gitReadOnlyFirstArg[args[0]]
}

func (t *GitTool) PreviewCall(argsJSON string) string {
	args, err := parseGitArgs(argsJSON)
	if err != nil {
		return fmt.Sprintf("git (invalid args: %v)", err)
	}
	cmd := "git " + strings.Join(args, " ")
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
