package agent

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// Background jobs belong to the parent session. A subagent shares the parent's
// tool instances, so without this a foreground child could start a job whose
// completion wakes the parent and which outlives the child.
func TestBuildChildRegistry_ChildrenGetForegroundOnlyRunBash(t *testing.T) {
	jobs := NewShellJobs(&AgentTool{Tasks: subagents.NewRegistry(), AllowBackground: true})
	parent := NewRegistry()
	parentBash := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: jobs}
	parent.Register(parentBash)
	parent.Register(&ProcessTool{Jobs: jobs})

	a := &AgentTool{ParentRegistry: parent}
	child := a.buildChildRegistry(&subagents.AgentConfig{})

	if _, ok := child.Get(processToolName); ok {
		t.Errorf("a child must not get the process tool")
	}
	got, ok := child.Get("run_bash")
	if !ok {
		t.Fatalf("a child must keep run_bash")
	}
	childBash := got.(*RunBashTool)
	if childBash.Jobs != nil {
		t.Errorf("a child's run_bash must not host background jobs")
	}
	if childBash.Cwd != parentBash.Cwd {
		t.Errorf("the child's run_bash must keep sharing the parent's cwd reference")
	}
	if _, ok := childBash.Schema()["properties"].(map[string]any)["background"]; ok {
		t.Errorf("the child's schema must not offer background")
	}
	if parentBash.Jobs == nil {
		t.Errorf("building a child registry must not mutate the parent's tool")
	}
}
