package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yottadynamics/yottacode/internal/github"
)

// git_push is the third composite in the commit/create-pr/push
// trio. Wraps `git push` with three guarantees the model can't
// honor in prose:
//
//  1. Upstream-aware: detects whether the current branch has an
//     upstream configured and adds `-u origin HEAD` only on first
//     push. The legacy markdown directives left this to the model
//     (which sometimes guessed wrong and produced a confusing
//     "no upstream" error).
//  2. Detached-HEAD early exit: returns a typed validation error
//     before invoking git, so the modal never fires on a state
//     that can't push.
//  3. No force-push surface: --force / --force-with-lease are not
//     accepted by this tool. The unified `git` tool remains the
//     escape hatch for power users; procedural /git-push is the
//     safe surface only.
//
// Optional PR-URL lookup via github.Interface after a successful
// push lets /git-push surface "PR updated: <url>" as a courtesy
// footer without a separate model round trip.

// GitPushTool runs `git push` for the current branch and (best
// effort) looks up the PR for the pushed branch via the typed
// github.Interface so /git-push can surface a "PR updated"
// footer when a PR exists. GH is optional — if nil, the PR
// lookup is skipped silently.
type GitPushTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *GitPushTool) Name() string { return "git_push" }

func (t *GitPushTool) Description() string {
	return "Push the current branch to origin. Detects whether the branch " +
		"already tracks an upstream and adds `-u origin HEAD` only on " +
		"first push. Returns a typed envelope with the pushed branch, " +
		"whether the upstream was newly set, the verbatim git output, and " +
		"(best effort) the URL of the existing PR for the branch so " +
		"callers can surface 'PR updated: <url>' as a courtesy. " +
		"Detached HEAD is rejected deterministically before git is " +
		"invoked. Force-push is not supported via this tool — use the " +
		"unified `git` tool for that."
}

func (t *GitPushTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GitPushTool) RequiresApproval(string) bool { return true }

func (t *GitPushTool) PreviewCall(string) string {
	// We can't render the exact subcommand here (set-upstream vs
	// not depends on a runtime probe), but the bare verb is enough
	// for the approval modal preview line — the modal also renders
	// the rendered command as a separate row.
	return "git_push()"
}

// PushResult is the typed envelope the tool returns. Same shape
// rationale as CommitResult / PRCreateResult: callers branch on
// typed fields rather than parsing err strings. Reason
// discriminates the failure mode so the slash directive can route
// each branch to the right surface.
type PushResult struct {
	Pushed      bool
	Branch      string
	SetUpstream bool   // true when the push added -u origin HEAD
	GitOutput   string // verbatim stdout+stderr from git, capped
	GitError    string // populated when git exited non-zero
	Detached    bool   // true when HEAD is detached (no branch)
	PRURL       string // best-effort: populated when a PR exists for the branch
	PRNumber    int    // populated alongside PRURL
}

func (t *GitPushTool) Execute(ctx context.Context, _ string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git_push: git binary not found in PATH")
	}
	res, err := PushBranch(ctx, t.Cwd.Get(), t.GH)
	if err != nil {
		return "", fmt.Errorf("git_push: %w", err)
	}
	return renderPushResult(res), nil
}

const pushOutputCap = 16 * 1024 // 16 KiB — covers verbose git push output

// PushBranch is the deterministic core of git_push. Returns a
// typed PushResult; errors return only on infrastructure
// failures (git binary missing, etc.). Detached-HEAD, missing
// remote, and authentication failures populate the envelope.
//
// The PR lookup is best-effort: a missing PR or an unreachable
// github.Interface leaves PRURL empty and never blocks the push
// result. The push itself is the load-bearing piece.
func PushBranch(ctx context.Context, cwd string, client github.Interface) (PushResult, error) {
	var res PushResult

	branch, err := currentBranchOrEmpty(ctx, cwd)
	if err != nil {
		return res, err
	}
	if branch == "" {
		res.Detached = true
		return res, nil
	}
	res.Branch = branch

	hasUpstream := branchHasUpstream(ctx, cwd, branch)
	args := []string{"push"}
	if !hasUpstream {
		args = append(args, "-u", "origin", "HEAD")
		res.SetUpstream = true
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	combined := strings.TrimRight(stdout.String()+stderr.String(), "\n")
	res.GitOutput, _ = capString(combined, pushOutputCap)

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.GitError = fmt.Sprintf("exit %d", exitErr.ExitCode())
			return res, nil
		}
		return res, fmt.Errorf("git push: %w", runErr)
	}

	res.Pushed = true

	// Best-effort PR-URL lookup. A missing PR or gh-unavailable
	// silently leaves PRURL empty — the push succeeded, the URL
	// footer is a nice-to-have not a load-bearing field.
	if client != nil {
		pr, err := client.ReadPR(ctx, github.ReadPRRequest{Ref: branch})
		if err == nil {
			res.PRURL = pr.URL
			res.PRNumber = pr.Number
		}
	}

	return res, nil
}

// currentBranchOrEmpty returns the current branch name, or empty
// string when HEAD is detached. Distinguishes the two so callers
// can populate the Detached flag explicitly rather than
// interpreting an opaque rev-parse failure.
func currentBranchOrEmpty(ctx context.Context, cwd string) (string, error) {
	out, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// rev-parse only fails on a non-repo or corrupt repo —
		// both are infrastructure errors, not detached HEAD.
		return "", fmt.Errorf("current branch: %w", err)
	}
	name := strings.TrimSpace(out)
	if name == "HEAD" {
		// rev-parse returns the literal string "HEAD" when the
		// working tree is in detached state — that's the
		// signal.
		return "", nil
	}
	return name, nil
}

// branchHasUpstream reports whether the named branch has an
// upstream configured. Used to decide between `git push` and
// `git push -u origin HEAD`. A failed check (no upstream) is
// the negative answer, not an error — that's the case where we
// add the -u flag.
func branchHasUpstream(ctx context.Context, cwd, branch string) bool {
	_, err := gitOutput(ctx, cwd,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name",
		branch+"@{upstream}")
	return err == nil
}

// renderPushResult shapes the envelope for the model. Field
// names match the PushResult struct so the slash directive's
// branching reads against the same names the typed struct
// exposes.
func renderPushResult(r PushResult) string {
	var b strings.Builder
	switch {
	case r.Detached:
		b.WriteString("pushed=false reason=detached_head\n")
		b.WriteString("Hint: switch to a branch with `git switch <name>` or create one before pushing.\n")
	case r.GitError != "":
		fmt.Fprintf(&b, "pushed=false reason=git_error error=%s\n", r.GitError)
		b.WriteString("--- git output ---\n")
		b.WriteString(r.GitOutput)
		if !strings.HasSuffix(r.GitOutput, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end git output ---\n")
		b.WriteString("Do NOT auto-retry, force-push, or rebase to \"fix\" the failure.\n")
	case r.Pushed:
		fmt.Fprintf(&b, "pushed=true branch=%s set_upstream=%v\n", r.Branch, r.SetUpstream)
		if r.PRURL != "" {
			fmt.Fprintf(&b, "pr_number=%d pr_url=%s\n", r.PRNumber, r.PRURL)
		} else {
			b.WriteString("pr_url=\nhint=no PR exists for this branch yet — use /git-create-pr to open one\n")
		}
	default:
		b.WriteString("pushed=false reason=unknown\n")
	}
	return b.String()
}

