package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ShellOut is the Interface implementation that wraps the local `gh`
// CLI. Acts as the foundation hook so procedural callers can depend
// on the typed Interface from day one; v0.5.0 swaps this for a
// `go-github` typed client without callers changing.
//
// TODO(v0.5.0): replace with go-github typed client. See
// yottacode-roadmap/github-integration.md — the typed client lands
// with auth precedence (GITHUB_TOKEN → gh auth token → file), an
// in-session cache, and the Github(<verb>) permissions rule shape.
// The Interface shape above is sized so that swap is a registration
// change (in internal/tui/run.go) and nothing more.
//
// Cwd pins the directory `gh` runs in so its inferred-repo behavior
// (gh reads the cwd's git remote when --repo isn't passed) does
// what the user expects. An empty Cwd lets gh fall back to the
// process's working directory.
type ShellOut struct {
	Cwd string
}

// CreatePR runs `gh pr create` with the request's fields. The body
// goes through `--body-file -` on stdin so backticks, dollar signs,
// and quote characters in the body pass through unmangled (matching
// the legacy markdown directive's heredoc shape, but without a shell
// in the loop).
//
// Availability check fires first: a missing gh binary or an
// unauthenticated install returns ErrGhUnavailable so the procedural
// /create-pr can fall through to a draft-only preview instead of
// surfacing a confusing exec failure.
func (s *ShellOut) CreatePR(ctx context.Context, req CreatePRRequest) (CreatePRResult, error) {
	var res CreatePRResult

	if err := validateCreateRequest(req); err != nil {
		return res, err
	}
	if err := checkGhAvailable(ctx); err != nil {
		return res, err
	}

	args := buildCreateArgs(req)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = s.Cwd
	cmd.Stdin = strings.NewReader(req.Body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		// gh prints helpful diagnostics on stderr; surface both
		// streams concatenated so the caller sees the cause without
		// having to know which side gh chose.
		out := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return res, fmt.Errorf("gh pr create: exit %d: %s",
				exitErr.ExitCode(), out)
		}
		return res, fmt.Errorf("gh pr create: %w (output: %s)", runErr, out)
	}

	out := strings.TrimSpace(stdout.String())
	res.URL = parsePRURL(out)
	res.Number = parsePRNumber(res.URL)
	if res.URL == "" {
		// gh succeeded but we couldn't pick a URL out of stdout —
		// surface as an error so the caller doesn't claim success with
		// an empty result. Falls through to the caller's error path,
		// which prints whatever gh said verbatim.
		return res, fmt.Errorf("gh pr create: succeeded but no PR URL in output: %q", out)
	}
	return res, nil
}

// validateCreateRequest enforces the fields the typed surface requires.
// Owner/Repo intentionally optional (gh infers); the v0.5.0 typed
// client will require both for the same call.
func validateCreateRequest(req CreatePRRequest) error {
	if strings.TrimSpace(req.Base) == "" {
		return errors.New("CreatePRRequest: Base is required")
	}
	if strings.TrimSpace(req.Title) == "" {
		return errors.New("CreatePRRequest: Title is required")
	}
	if strings.TrimSpace(req.Body) == "" {
		return errors.New("CreatePRRequest: Body is required")
	}
	return nil
}

// buildCreateArgs assembles the gh argv. Pulled out so tests can
// assert on the exact flag shape without invoking gh.
//
// `--body-file -` reads body from stdin (handled by the caller).
// `--repo owner/repo` is only set when both fields are populated so
// gh's cwd-inference path still works when callers want it.
func buildCreateArgs(req CreatePRRequest) []string {
	args := []string{"pr", "create",
		"--base", req.Base,
		"--title", req.Title,
		"--body-file", "-",
	}
	if req.Head != "" {
		args = append(args, "--head", req.Head)
	}
	if req.Owner != "" && req.Repo != "" {
		args = append(args, "--repo", req.Owner+"/"+req.Repo)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	return args
}

// prURLPattern matches the canonical GitHub PR URL gh emits on
// success. We extract the URL rather than relying on stdout being
// exactly a URL because gh sometimes prints a "Creating..." line
// or a follow-up tip alongside the URL.
var prURLPattern = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/pull/\d+`)

// parsePRURL pulls the canonical PR URL out of gh's stdout. Returns
// "" when no URL is present — the caller's "no URL in output"
// branch handles that (it shouldn't happen on a clean success).
func parsePRURL(s string) string {
	return prURLPattern.FindString(s)
}

// parsePRNumber extracts the trailing /pull/<n> from a URL. Returns
// 0 when the URL doesn't match (which means parsePRURL already
// returned "" — so Number is effectively never relied on without
// URL being populated).
func parsePRNumber(url string) int {
	if url == "" {
		return 0
	}
	if i := strings.LastIndex(url, "/"); i >= 0 && i+1 < len(url) {
		n, err := strconv.Atoi(url[i+1:])
		if err == nil {
			return n
		}
	}
	return 0
}

// checkGhAvailable verifies the gh binary is on PATH and that
// `gh auth status` reports an authenticated session. Returns
// ErrGhUnavailable on either failure so callers can branch
// without parsing the error string.
//
// Cheap: gh auth status is a local read of ~/.config/gh/hosts.yml,
// not a GitHub round-trip. Safe to run on every CreatePR call.
func checkGhAvailable(ctx context.Context) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return ErrGhUnavailable
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	if err := cmd.Run(); err != nil {
		return ErrGhUnavailable
	}
	return nil
}

// IsGhAvailable is the exported variant of checkGhAvailable so
// callers outside this package (gh_pr_context tool) can render
// "GhAvailable=false" in their snapshot without duplicating the
// PATH + auth check.
func IsGhAvailable(ctx context.Context) bool {
	return checkGhAvailable(ctx) == nil
}
