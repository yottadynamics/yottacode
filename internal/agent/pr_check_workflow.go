package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yottadynamics/yottacode/internal/github"
)

const (
	defaultPRCheckLogTailLines = 240
	maxPRCheckLogTailLines     = 2000
)

// PRCheckLogsTool fetches failed GitHub Actions job logs for a PR without
// shelling out to `gh run view --log-failed | tail`. It is read-only, but
// intentionally bounded so failed logs cannot flood the model context.
type PRCheckLogsTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *PRCheckLogsTool) Name() string { return "pr_check_logs" }

func (t *PRCheckLogsTool) Description() string {
	return "Fetch failed GitHub Actions job log tails for a pull request. " +
		"Use this instead of `gh run view --log-failed | tail -N` from run_bash " +
		"after `pr_watch_checks` or `pr_review_context` reports failing CI. " +
		"Ref is the PR number or branch name; empty defaults to the current branch. " +
		"max_lines defaults to 240 and is capped at 2000. Read-only, no approval."
}

func (t *PRCheckLogsTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
			"max_lines": map[string]any{
				"type":        "integer",
				"description": "Maximum log lines per failed job. Default 240; capped at 2000.",
			},
		},
	}
}

func (t *PRCheckLogsTool) RequiresApproval(string) bool { return false }

func (t *PRCheckLogsTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref      string `json:"ref"`
		MaxLines int    `json:"max_lines"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	parts := []string{}
	if strings.TrimSpace(a.Ref) != "" {
		parts = append(parts, "ref="+a.Ref)
	}
	if a.MaxLines > 0 {
		parts = append(parts, fmt.Sprintf("max_lines=%d", a.MaxLines))
	}
	return "pr_check_logs(" + strings.Join(parts, ", ") + ")"
}

func (t *PRCheckLogsTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("pr_check_logs: no GitHub adapter configured")
	}
	var a struct {
		Ref      string `json:"ref"`
		MaxLines int    `json:"max_lines"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_check_logs: detect current branch: %w", err)
	}
	a.MaxLines = boundedInt(a.MaxLines, defaultPRCheckLogTailLines, 0, maxPRCheckLogTailLines)
	pr, err := t.GH.ReadPR(ctx, github.ReadPRRequest{Ref: ref})
	if err != nil {
		return renderPRCheckLogsError(ref, err), nil
	}
	logs, err := t.GH.ListFailedWorkflowJobLogTails(ctx, github.FailedWorkflowLogsRequest{HeadSHA: pr.HeadSHA, TailLines: a.MaxLines, MaxRuns: 10})
	if err != nil {
		return renderPRCheckLogsError(ref, err), nil
	}
	return renderPRCheckLogs(ref, logs), nil
}

func renderPRCheckLogsError(ref string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nref=%s\n", ref)
	switch {
	case errors.Is(err, github.ErrGitHubUnavailable):
		b.WriteString("github_unavailable=true\n")
	case errors.Is(err, github.ErrPRNotFound):
		b.WriteString("not_found=true\n")
	default:
		b.WriteString("github_error=true\n")
		fmt.Fprintf(&b, "error=%s\n", err)
	}
	return b.String()
}

func renderPRCheckLogs(ref string, logs github.FailedWorkflowLogsResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## state\nref=%s\nnot_found=false\ngithub_unavailable=false\nfailed_jobs=%d\n", ref, len(logs.Jobs))
	b.WriteString("\n## failed_job_logs\n")
	if len(logs.Jobs) == 0 {
		b.WriteString("(none)\n")
		return b.String()
	}
	for _, log := range logs.Jobs {
		fmt.Fprintf(&b, "### %s\nrun_id=%d\njob_id=%d\nworkflow=%s\nconclusion=%s\nurl=%s\nlines=%d\nlog=|\n",
			log.JobName, log.WorkflowRunID, log.JobID, log.WorkflowName, log.Conclusion, log.JobURL, len(log.LogTail))
		if log.LogError != "" {
			fmt.Fprintf(&b, "  log_error=%s\n", log.LogError)
			continue
		}
		for _, line := range log.LogTail {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// PRRerunChecksTool re-runs failed GitHub Actions jobs for a PR. It is
// approval-required because it mutates remote CI state and can consume CI minutes.
type PRRerunChecksTool struct {
	Cwd *CwdRef
	GH  github.Interface
}

func (t *PRRerunChecksTool) Name() string { return "pr_rerun_checks" }

func (t *PRRerunChecksTool) Description() string {
	return "Rerun failed GitHub Actions jobs for a pull request. Ref is the PR " +
		"number or branch name; empty defaults to the current branch. This uses " +
		"GitHub's failed-jobs rerun endpoint instead of rerunning successful jobs. " +
		"Approval-required because it mutates remote CI state and may consume CI minutes."
}

func (t *PRRerunChecksTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "PR number (\"17\") or branch name. Empty defaults to current branch.",
			},
		},
	}
}

func (t *PRRerunChecksTool) RequiresApproval(string) bool { return true }

func (t *PRRerunChecksTool) PreviewCall(argsJSON string) string {
	var a struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Ref) == "" {
		return "pr_rerun_checks()"
	}
	return fmt.Sprintf("pr_rerun_checks(ref=%s)", a.Ref)
}

func (t *PRRerunChecksTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t.GH == nil {
		return "", errors.New("pr_rerun_checks: no GitHub adapter configured")
	}
	var a struct {
		Ref string `json:"ref"`
	}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &a)
	}
	ref, err := livePRRefOrExplicit(ctx, t.Cwd, a.Ref)
	if err != nil {
		return "", fmt.Errorf("pr_rerun_checks: detect current branch: %w", err)
	}
	res, err := t.GH.RerunFailedPRChecks(ctx, github.ReadPRRequest{Ref: ref})
	if err != nil {
		return renderPRRerunChecksError(ref, err), nil
	}
	return renderPRRerunChecks(ref, res), nil
}

func renderPRRerunChecksError(ref string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rerun=false\nref=%s\n", ref)
	switch {
	case errors.Is(err, github.ErrGitHubUnavailable):
		b.WriteString("reason=github_unavailable\n")
	case errors.Is(err, github.ErrPRNotFound):
		b.WriteString("reason=not_found\n")
	default:
		b.WriteString("reason=github_error\n")
		fmt.Fprintf(&b, "error=%s\n", err)
	}
	return b.String()
}

func renderPRRerunChecks(ref string, res github.RerunFailedPRChecksResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rerun=true\nref=%s\ncount=%d\n", ref, res.Count)
	if len(res.RunIDs) > 0 {
		ids := make([]string, 0, len(res.RunIDs))
		for _, id := range res.RunIDs {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		fmt.Fprintf(&b, "run_ids=%s\n", strings.Join(ids, ","))
	}
	if res.Count == 0 {
		b.WriteString("message=no failed workflow runs found for this PR head\n")
	} else {
		b.WriteString("message=GitHub accepted failed-job rerun requests; use pr_watch_checks to monitor the new run state.\n")
	}
	return b.String()
}
