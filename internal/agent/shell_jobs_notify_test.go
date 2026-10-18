//go:build unix

package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// ACP supports background jobs but wires no completion callback, so a promised
// notice would silently never arrive. The request must be downgraded and the
// model told to poll instead.
func TestShellJobs_NotifyDowngradedWhereCompletionsCannotBeDelivered(t *testing.T) {
	a := &AgentTool{Tasks: subagents.NewRegistry(), AllowBackground: true} // no SetBackgroundDoneCallback
	jobs := NewShellJobs(a)
	t.Cleanup(func() { _ = jobs.Cleanup(context.Background()) })
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: jobs}

	out, err := tool.Execute(context.Background(), `{"command":"echo hi","background":true,"notify_on_complete":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "You will be notified") {
		t.Fatalf("must not promise a notice that cannot be delivered:\n%s", out)
	}
	for _, want := range []string{"unavailable in this session", "You will NOT be told", "process(action="} {
		if !strings.Contains(out, want) {
			t.Errorf("start result missing %q:\n%s", want, out)
		}
	}
	id := sessionIDFrom(t, out)
	j, _ := jobs.lookup(id)
	waitJob(t, j)
	if task, _ := a.Tasks.Get(id); task.NotifyOnDone {
		t.Errorf("the task record must not claim a wake that will never happen")
	}
	// Polling still works, which is the path the model was told to use.
	if poll, _ := jobs.Poll(id); !strings.Contains(poll, "exited (exit=0)") {
		t.Errorf("poll:\n%s", poll)
	}
}

func TestShellJobs_PromotionWithoutDeliveryTellsModelToPoll(t *testing.T) {
	a := &AgentTool{Tasks: subagents.NewRegistry(), AllowBackground: true}
	jobs := NewShellJobs(a)
	t.Cleanup(func() { _ = jobs.Cleanup(context.Background()) })
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: jobs}

	over := runBashMaxTimeout.Milliseconds() + 1
	out, err := tool.Execute(context.Background(), `{"command":"true","timeout_ms":`+strconv.FormatInt(over, 10)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "promoted to a background job") || !strings.Contains(out, "You will NOT be told") {
		t.Fatalf("promotion result must explain polling:\n%s", out)
	}
}
