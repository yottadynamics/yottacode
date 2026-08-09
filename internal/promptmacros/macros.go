// Package promptmacros holds the "prompt macro" slash commands shared
// between internal/tui and internal/acp — the bucket-A commands
// (/git-commit, /git-push, /git-create-pr, /git-update-pr,
// /git-create-issue, /git-review-pr, /code-review,
// /git-implement-issue, /init) that translate a slash invocation into
// a directive prompt for the agent loop to run as a normal turn.
//
// Each Macro.Build absorbs both the arg-parsing/validation and the
// prompt construction that, before this package existed, were split
// between a TUI handler (internal/tui/cmd_*.go) and an unexported
// directive-builder function. That split meant only internal/tui
// could invoke these commands; internal/acp had no way to reuse them.
// Build's signature — (cwd, args) in, (prompt, error) out — works
// identically for both callers: an error means "don't start a turn",
// success means "submit this prompt text as the turn's user message."
package promptmacros

import (
	"fmt"
	"strconv"
	"strings"
)

// Macro describes one bucket-A slash command.
type Macro struct {
	Name        string
	Description string
	// ArgHint is empty when the command takes no args.
	ArgHint string
	// Build parses args and returns the prompt text to submit as the
	// turn's user message. Returns an error for invalid/missing
	// required input (e.g. git-implement-issue with no issue number)
	// — callers render that as an error and never start a turn on it.
	Build func(cwd string, args []string) (string, error)
}

var registry = []Macro{
	{
		Name:        "git-commit",
		Description: "compose and run a one-line git commit",
		Build: func(cwd string, args []string) (string, error) {
			return GitCommitDirective(), nil
		},
	},
	{
		Name:        "git-push",
		Description: "push the current branch to origin (sets upstream on first push; surfaces the PR URL when one exists)",
		Build: func(cwd string, args []string) (string, error) {
			return GitPushDirective(), nil
		},
	},
	{
		Name:        "git-create-pr",
		Description: "open a pull request for the current branch",
		ArgHint:     "[base]",
		Build: func(cwd string, args []string) (string, error) {
			base := ""
			if len(args) > 0 {
				base = strings.TrimSpace(args[0])
			}
			return GitCreatePRDirective(base), nil
		},
	},
	{
		Name:        "git-update-pr",
		Description: "refresh a PR's title and body to match the current commit list",
		ArgHint:     "[ref]",
		Build: func(cwd string, args []string) (string, error) {
			ref := ""
			if len(args) > 0 {
				ref = strings.TrimSpace(args[0])
			}
			return GitUpdatePRDirective(ref), nil
		},
	},
	{
		Name:        "git-create-issue",
		Description: "create a GitHub issue in the current repo",
		ArgHint:     "[title]",
		Build: func(cwd string, args []string) (string, error) {
			return GitCreateIssueDirective(IssueTitleFromArgs(args)), nil
		},
	},
	{
		Name:        "git-review-pr",
		Description: "review a pull request (number or branch; defaults to current branch's PR)",
		ArgHint:     "[ref]",
		Build: func(cwd string, args []string) (string, error) {
			ref := ""
			if len(args) > 0 {
				ref = strings.TrimSpace(args[0])
			}
			return GitReviewPRDirective(ref), nil
		},
	},
	{
		Name:        "code-review",
		Description: "multi-agent review of the current diff — effort low · medium · high (background subagents GA)",
		ArgHint:     "[low|medium|high]",
		Build: func(cwd string, args []string) (string, error) {
			effort, notice := ParseEffort(args)
			if notice != "" {
				return "", fmt.Errorf("%s", notice)
			}
			return CodeReviewDirective(effort), nil
		},
	},
	{
		Name:        "git-implement-issue",
		Description: "implement a GitHub issue end-to-end: fetch → plan → branch → code → tests → commit → push → draft PR",
		ArgHint:     "<n>",
		Build: func(cwd string, args []string) (string, error) {
			if len(args) == 0 {
				return "", fmt.Errorf("usage: /git-implement-issue <issue-number>")
			}
			raw := strings.TrimSpace(args[0])
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return "", fmt.Errorf("issue number must be a positive integer; got %s", raw)
			}
			return GitImplementIssueDirective(n), nil
		},
	},
	{
		Name:        "init",
		Description: "draft .yottacode/YOTTACODE.md from the current repo",
		Build: func(cwd string, args []string) (string, error) {
			return BuildInitPrompt(cwd), nil
		},
	},
}

// All returns every registered macro, in the fixed registration order
// above (git-* family first, then code-review, then init).
func All() []Macro {
	out := make([]Macro, len(registry))
	copy(out, registry)
	return out
}

// Get looks up a macro by name.
func Get(name string) (Macro, bool) {
	for _, m := range registry {
		if m.Name == name {
			return m, true
		}
	}
	return Macro{}, false
}

// MustGet is Get for callers (package-init-time registry wiring) where
// a missing name is a programming error, not a runtime condition to
// handle gracefully.
func MustGet(name string) Macro {
	m, ok := Get(name)
	if !ok {
		panic("promptmacros: no macro named " + name)
	}
	return m
}
