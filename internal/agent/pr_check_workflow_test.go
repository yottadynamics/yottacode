package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/github"
)

func TestPRCheckLogsTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{
		readPRRes: github.PRDetails{HeadSHA: "sha"},
		failedLogsRes: github.FailedWorkflowLogsResult{Jobs: []github.FailedWorkflowJobLog{{
			WorkflowRunID: 101,
			JobID:         202,
			JobName:       "test",
			WorkflowName:  "CI",
			Conclusion:    "FAILURE",
			JobURL:        "https://github.com/o/r/actions/runs/101/job/202",
			LogTail:       []string{"panic: boom", "FAIL"},
		}},
		},
	}
	tool := &PRCheckLogsTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{"ref":"29","max_lines":12}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gh.failedLogsReq.HeadSHA != "sha" || gh.failedLogsReq.TailLines != 12 {
		t.Fatalf("request not forwarded: req=%+v", gh.failedLogsReq)
	}
	for _, want := range []string{"## state", "failed_jobs=1", "### test", "run_id=101", "panic: boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestPRCheckLogsTool_DefaultsMaxLines(t *testing.T) {
	gh := &fakeGH{readPRRes: github.PRDetails{HeadSHA: "sha"}}
	tool := &PRCheckLogsTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	_, err := tool.Execute(context.Background(), `{"ref":"29"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gh.failedLogsReq.TailLines != defaultPRCheckLogTailLines {
		t.Fatalf("max_lines = %d, want default %d", gh.failedLogsReq.TailLines, defaultPRCheckLogTailLines)
	}
}

func TestPRCheckLogsTool_NotApprovalRequired(t *testing.T) {
	if (&PRCheckLogsTool{}).RequiresApproval("") {
		t.Errorf("pr_check_logs is read-only and must not require approval")
	}
}

func TestPRRerunChecksTool_RoundsThroughTool(t *testing.T) {
	gh := &fakeGH{rerunRes: github.RerunFailedPRChecksResult{RunIDs: []int64{101, 102}, Count: 2}}
	tool := &PRRerunChecksTool{Cwd: NewCwdRef(t.TempDir()), GH: gh}
	out, err := tool.Execute(context.Background(), `{"ref":"29"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gh.rerunReq.Ref != "29" {
		t.Fatalf("ref not forwarded: %+v", gh.rerunReq)
	}
	for _, want := range []string{"rerun=true", "count=2", "run_ids=101,102", "pr_watch_checks"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestPRRerunChecksTool_RequiresApproval(t *testing.T) {
	if !(&PRRerunChecksTool{}).RequiresApproval("") {
		t.Errorf("pr_rerun_checks mutates remote CI state and must require approval")
	}
}
