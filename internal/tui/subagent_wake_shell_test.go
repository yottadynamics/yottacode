package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestBuildSubagentWakeMessage_ShellJob(t *testing.T) {
	msg := buildSubagentWakeMessage([]agent.SubagentBackgroundDone{
		{TaskID: "aaaa1111bbbb2222", AgentType: agent.ShellTaskType, Result: "exit=0 · $ make\n--- output ---\nbuilt", NotifyOnDone: true},
	})
	for _, want := range []string{"background shell job you started has finished", "session_id aaaa1111bbbb2222", "built"} {
		if !strings.Contains(msg, want) {
			t.Errorf("shell wake missing %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "subagent") {
		t.Errorf("a shell job is not a subagent; got:\n%s", msg)
	}
}

func TestBuildSubagentWakeMessage_MixedShellAndSubagentSaysTasks(t *testing.T) {
	wakes := []agent.SubagentBackgroundDone{
		{TaskID: "aaaa1111bbbb2222", AgentType: agent.ShellTaskType, Result: "exit=0"},
		{TaskID: "cccc3333dddd4444", AgentType: "review", Result: "ok"},
	}
	msg := buildSubagentWakeMessage(wakes)
	if !strings.Contains(msg, "2 background tasks you started have finished") {
		t.Errorf("mixed wake should say tasks; got:\n%s", msg)
	}
	if !strings.Contains(msg, "Shell job: session_id aaaa1111bbbb2222") || !strings.Contains(msg, "Subagent: review") {
		t.Errorf("each entry should keep its own header; got:\n%s", msg)
	}
	if allShellWakes(wakes) || !anyShellWake(wakes) {
		t.Errorf("allShellWakes/anyShellWake misclassified a mixed batch")
	}
}

func TestAllShellWakes_EmptyIsFalse(t *testing.T) {
	if allShellWakes(nil) {
		t.Errorf("an empty batch is not a shell batch")
	}
}
