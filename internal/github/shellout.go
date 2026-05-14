package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// prViewFields is the JSON field set ReadPR + ListPRChecks need
// from `gh pr view`. Pinned here (not constructed at the call site)
// so adding fields is one place and the test that exercises parsing
// stays bounded by a known shape.
//
// statusCheckRollup is the GitHub-side enumeration of every check
// run hanging off the PR's head SHA; including it in ReadPR's
// underlying call means ListPRChecks can reuse the same response
// without a second network round-trip (the ShellOut implementation
// makes that opportunistic batching decision; the typed v0.5.0
// client will likely do the same on the GraphQL side).
const prViewFields = "number,title,body,state,isDraft,baseRefName,headRefName,headRefOid,mergeable,author,url,labels,statusCheckRollup"

// ReadPR fetches typed PR metadata by shelling out to
// `gh pr view <ref> --json <fields>`. Returns ErrPRNotFound when
// gh reports the PR doesn't exist; ErrGhUnavailable when the
// local environment can't make the call.
func (s *ShellOut) ReadPR(ctx context.Context, req ReadPRRequest) (PRDetails, error) {
	var details PRDetails
	if err := checkGhAvailable(ctx); err != nil {
		return details, err
	}
	args := buildPRViewArgs(req)
	stdout, stderr, err := s.runGh(ctx, args, "")
	if err != nil {
		if classifyGhNotFound(stderr) {
			return details, ErrPRNotFound
		}
		return details, fmt.Errorf("gh pr view: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return parsePRDetails(stdout)
}

// ReadPRDiff fetches the unified diff via `gh pr diff <ref>`.
// gh writes the diff to stdout directly — no JSON parse step.
func (s *ShellOut) ReadPRDiff(ctx context.Context, req ReadPRRequest) (string, error) {
	if err := checkGhAvailable(ctx); err != nil {
		return "", err
	}
	args := buildPRDiffArgs(req)
	stdout, stderr, err := s.runGh(ctx, args, "")
	if err != nil {
		if classifyGhNotFound(stderr) {
			return "", ErrPRNotFound
		}
		return "", fmt.Errorf("gh pr diff: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

// UpdatePR rewrites an existing PR's title and body via GitHub's
// REST API: `gh api repos/{owner}/{repo}/pulls/{n} -X PATCH
// --input -` with a JSON payload on stdin.
//
// This route deliberately bypasses `gh pr edit`. As of gh 2.86
// (Feb 2026) `pr edit` still queries the `projectCards` GraphQL
// field in its response shape, which GitHub returns as a
// deprecation *error* (not warning) for repos with any classic
// Projects history — causing `pr edit` to fail with a non-zero
// exit despite the mutation itself succeeding. The REST endpoint
// has no equivalent of that field, so the round trip stays clean.
//
// JSON payload on stdin (rather than `-f title=… -f body=…`)
// because `-f` form-encodes through shell argv, which means
// arbitrary body content (backticks, dollar signs, newlines,
// quotes) would need careful escaping. `--input -` accepts a
// JSON object on stdin and forwards it verbatim — same rationale
// as `--body-file -` in the CreatePR path.
//
// Validation is the caller's responsibility (gh_pr_update enforces
// title rules + non-empty body in Go before dialing). ErrPRNotFound
// + ErrGhUnavailable surface the same way as the other Interface
// methods so callers branch on typed errors.
func (s *ShellOut) UpdatePR(ctx context.Context, req UpdatePRRequest) (UpdatePRResult, error) {
	var res UpdatePRResult
	if err := checkGhAvailable(ctx); err != nil {
		return res, err
	}

	owner, repo, number, err := s.resolvePRTarget(ctx, req)
	if err != nil {
		// resolvePRTarget already typed ErrPRNotFound / wrapped
		// generic errors with stderr context, so just propagate.
		return res, err
	}

	payload, err := json.Marshal(map[string]string{
		"title": req.Title,
		"body":  req.Body,
	})
	if err != nil {
		return res, fmt.Errorf("marshal update payload: %w", err)
	}

	args := []string{
		"api",
		fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number),
		"-X", "PATCH",
		"--input", "-",
	}
	stdout, stderr, err := s.runGh(ctx, args, string(payload))
	if err != nil {
		if classifyGhNotFound(stderr) {
			return res, ErrPRNotFound
		}
		return res, fmt.Errorf("gh api PATCH: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	parsed := parsePATCHResponse(stdout)
	res.URL = parsed.URL
	res.Number = parsed.Number
	return res, nil
}

// shortcutPRTarget returns (owner, repo, number, true) when the
// request carries enough info to skip a lookup round trip. False
// means the caller must dial gh to resolve. Extracted from
// resolvePRTarget so the fast-path policy is unit-testable
// without invoking gh.
func shortcutPRTarget(req UpdatePRRequest) (owner, repo string, number int, ok bool) {
	if req.Owner == "" || req.Repo == "" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(req.Ref))
	if err != nil {
		return "", "", 0, false
	}
	return req.Owner, req.Repo, n, true
}

// resolvePRTarget gets (owner, repo, number) for a PATCH call.
// Fast path: caller gave us everything explicitly — no gh round
// trip. Slow path: one `gh pr view <ref> --json url` call
// resolves the trio at once by parsing GitHub's canonical PR URL
// (`https://github.com/<owner>/<repo>/pull/<n>`). owner/repo in
// the URL is always the *base* repo (the one the PR lives in),
// which is exactly what the REST PATCH endpoint needs — same-repo
// and cross-fork PRs both produce the right target.
//
// Asking for `url` only (not `number` or repo fields) sidesteps
// gh's field-name churn and keeps the JSON shape we depend on as
// narrow as possible.
func (s *ShellOut) resolvePRTarget(ctx context.Context, req UpdatePRRequest) (owner, repo string, number int, err error) {
	if o, r, n, ok := shortcutPRTarget(req); ok {
		return o, r, n, nil
	}

	args := []string{"pr", "view"}
	if strings.TrimSpace(req.Ref) != "" {
		args = append(args, req.Ref)
	}
	if req.Owner != "" && req.Repo != "" {
		args = append(args, "--repo", req.Owner+"/"+req.Repo)
	}
	args = append(args, "--json", "url")

	stdout, stderr, runErr := s.runGh(ctx, args, "")
	if runErr != nil {
		if classifyGhNotFound(stderr) {
			return "", "", 0, ErrPRNotFound
		}
		return "", "", 0, fmt.Errorf("resolve PR target: %w (stderr: %s)",
			runErr, strings.TrimSpace(stderr))
	}

	target, perr := parsePRTarget(stdout)
	if perr != nil {
		return "", "", 0, perr
	}
	return target.Owner, target.Repo, target.Number, nil
}

// prTarget is the resolved (owner, repo, number) tuple. Internal
// shape only — callers receive it splayed across return values.
type prTarget struct {
	Owner  string
	Repo   string
	Number int
}

// prURLTarget extracts (owner, repo, number) from a canonical
// GitHub PR URL. Anchored regex to refuse near-misses (e.g. a URL
// pointing at /issues/<n> instead of /pull/<n>).
var prURLTarget = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)$`)

// parsePRTarget maps gh's `--json url` output into a prTarget by
// running prTargetFromURL on the extracted URL. Extracted from
// resolvePRTarget so the JSON-to-tuple mapping is testable in
// isolation.
func parsePRTarget(stdout string) (prTarget, error) {
	var raw struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return prTarget{}, fmt.Errorf("parse pr target: %w", err)
	}
	return prTargetFromURL(raw.URL)
}

// prTargetFromURL parses a canonical PR URL into the typed tuple.
// Pure function for unit testing — covers both same-repo and
// cross-fork cases (the URL embeds the base repo either way).
func prTargetFromURL(url string) (prTarget, error) {
	m := prURLTarget.FindStringSubmatch(url)
	if m == nil {
		return prTarget{}, fmt.Errorf("unexpected PR URL shape: %q", url)
	}
	number, err := strconv.Atoi(m[3])
	if err != nil {
		return prTarget{}, fmt.Errorf("parse PR number from URL %q: %w", url, err)
	}
	return prTarget{Owner: m[1], Repo: m[2], Number: number}, nil
}

// apiPullResponse is the slice of GitHub's REST API PATCH
// response we actually use. The full response is the entire
// updated PR object; we read html_url and number for the
// UpdatePRResult envelope and let everything else fall on the
// floor (json.Unmarshal ignores unknown fields).
type apiPullResponse struct {
	URL    string `json:"html_url"`
	Number int    `json:"number"`
}

// parsePATCHResponse pulls (URL, Number) out of gh api's PATCH
// response. Extracted for testability; on malformed JSON returns
// a zero-value response rather than an error so the caller's
// "succeeded but no URL" branch is the failure mode the model
// sees (matches the CreatePR shape).
func parsePATCHResponse(stdout string) apiPullResponse {
	var r apiPullResponse
	_ = json.Unmarshal([]byte(stdout), &r)
	return r
}

// ListPRChecks reuses the same `gh pr view --json statusCheckRollup`
// call as ReadPR. Keeps the shell-out cost to one process spawn
// when callers want both metadata and checks (gh_pr_review_context
// will fetch them together in a future revision; for now each is
// its own call). When the typed go-github client lands, both can
// share a GraphQL fragment trivially.
func (s *ShellOut) ListPRChecks(ctx context.Context, req ReadPRRequest) ([]CheckRun, error) {
	if err := checkGhAvailable(ctx); err != nil {
		return nil, err
	}
	args := buildPRViewArgs(req)
	stdout, stderr, err := s.runGh(ctx, args, "")
	if err != nil {
		if classifyGhNotFound(stderr) {
			return nil, ErrPRNotFound
		}
		return nil, fmt.Errorf("gh pr view: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return parsePRChecks(stdout)
}

// buildPRViewArgs assembles the gh argv for the JSON read.
// Pulled out so tests can assert the flag shape without invoking
// gh; same rationale as buildCreateArgs.
func buildPRViewArgs(req ReadPRRequest) []string {
	args := []string{"pr", "view"}
	if strings.TrimSpace(req.Ref) != "" {
		args = append(args, req.Ref)
	}
	if req.Owner != "" && req.Repo != "" {
		args = append(args, "--repo", req.Owner+"/"+req.Repo)
	}
	args = append(args, "--json", prViewFields)
	return args
}

// buildPRDiffArgs is the sibling for `gh pr diff`. No JSON flag
// (diff is plain text). Same Ref / Owner / Repo semantics.
func buildPRDiffArgs(req ReadPRRequest) []string {
	args := []string{"pr", "diff"}
	if strings.TrimSpace(req.Ref) != "" {
		args = append(args, req.Ref)
	}
	if req.Owner != "" && req.Repo != "" {
		args = append(args, "--repo", req.Owner+"/"+req.Repo)
	}
	return args
}

// runGh is the shared subprocess runner for read calls. Captures
// stdout and stderr separately so the not-found classifier can
// inspect stderr without polluting stdout's JSON. Returns
// (stdout, stderr, err).
func (s *ShellOut) runGh(ctx context.Context, args []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = s.Cwd
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// classifyGhNotFound checks whether gh's stderr indicates the PR
// (or repo) doesn't exist, so callers can return ErrPRNotFound
// instead of an opaque exit-non-zero. gh writes "no pull requests
// found" for branch-without-PR and "Could not resolve to a
// PullRequest" for an explicit number that doesn't exist.
func classifyGhNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no pull requests found") ||
		strings.Contains(s, "could not resolve to a pullrequest") ||
		strings.Contains(s, "no such pull request")
}

// ghPRViewJSON mirrors the shape gh emits for --json with the
// fields in prViewFields. Internal — never exported because the
// public surface is PRDetails / []CheckRun.
type ghPRViewJSON struct {
	Number      int                 `json:"number"`
	Title       string              `json:"title"`
	Body        string              `json:"body"`
	State       string              `json:"state"`
	IsDraft     bool                `json:"isDraft"`
	BaseRefName string              `json:"baseRefName"`
	HeadRefName string              `json:"headRefName"`
	HeadRefOid  string              `json:"headRefOid"`
	Mergeable   string              `json:"mergeable"`
	URL         string              `json:"url"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	StatusCheckRollup []ghCheckRunJSON `json:"statusCheckRollup"`
}

// ghCheckRunJSON covers both check-run and status-context entries
// in statusCheckRollup. gh normalizes the two into a single array
// with `__typename` discriminating; we use Name + State +
// Conclusion which both shapes populate consistently. CompletedAt
// and StartedAt are RFC3339 strings — parsed at the boundary.
type ghCheckRunJSON struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	State       string `json:"state"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
}

// parsePRDetails maps gh's JSON into our typed PRDetails. The
// labels-to-strings flatten and the author-login pluck are the
// only non-trivial parts; everything else is field copy.
func parsePRDetails(stdout string) (PRDetails, error) {
	var raw ghPRViewJSON
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return PRDetails{}, fmt.Errorf("parse pr view: %w", err)
	}
	out := PRDetails{
		Number:    raw.Number,
		Title:     raw.Title,
		Body:      raw.Body,
		State:     raw.State,
		Draft:     raw.IsDraft,
		BaseRef:   raw.BaseRefName,
		HeadRef:   raw.HeadRefName,
		HeadSHA:   raw.HeadRefOid,
		Mergeable: raw.Mergeable,
		Author:    raw.Author.Login,
		URL:       raw.URL,
	}
	for _, l := range raw.Labels {
		if l.Name != "" {
			out.Labels = append(out.Labels, l.Name)
		}
	}
	return out, nil
}

// parsePRChecks pulls just statusCheckRollup out of the JSON.
// Two enum shapes coexist in gh's rollup (check runs use State;
// statuses use Status); normalizeCheckState picks whichever is
// populated so callers don't have to.
func parsePRChecks(stdout string) ([]CheckRun, error) {
	var raw ghPRViewJSON
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("parse pr checks: %w", err)
	}
	out := make([]CheckRun, 0, len(raw.StatusCheckRollup))
	for _, c := range raw.StatusCheckRollup {
		out = append(out, CheckRun{
			Name:        c.Name,
			State:       normalizeCheckState(c),
			Conclusion:  c.Conclusion,
			StartedAt:   parseRFC3339(c.StartedAt),
			CompletedAt: parseRFC3339(c.CompletedAt),
		})
	}
	return out, nil
}

// normalizeCheckState picks the populated lifecycle field across
// gh's two rollup shapes. CheckRun uses Status ("QUEUED" /
// "IN_PROGRESS" / "COMPLETED"); StatusContext uses State
// ("PENDING" / "SUCCESS" / "FAILURE" / "ERROR"). We surface
// whichever the API filled in.
func normalizeCheckState(c ghCheckRunJSON) string {
	if c.Status != "" {
		return c.Status
	}
	return c.State
}

// parseRFC3339 is a best-effort timestamp parser. gh emits
// "2024-01-02T15:04:05Z"; an empty string means "not started yet"
// and returns the zero time. Anything else garbled also returns
// zero — callers IsZero-check before formatting anyway, so
// silently swallowing the parse error is safe and keeps the
// snapshot intact.
func parseRFC3339(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
