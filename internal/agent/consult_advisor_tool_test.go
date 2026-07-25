package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func TestConsultAdvisorTool_ReturnsAdvisorAnswer(t *testing.T) {
	advisor := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("try the smaller interface")}}}
	tool := &ConsultAdvisorTool{Advisor: advisor, Model: "smart"}

	out, err := tool.Execute(context.Background(), `{"question":"how should I split this?","context":"one big type"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "try the smaller interface" {
		t.Errorf("out = %q", out)
	}
}

func TestConsultAdvisorTool_RequiresQuestion(t *testing.T) {
	tool := &ConsultAdvisorTool{Advisor: &scriptedStreamer{}}
	_, err := tool.Execute(context.Background(), `{"question":" "}`)
	if err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("expected question-required error, got %v", err)
	}
}

func TestAgentTool_ChildRegistryAddsConsultAdvisor(t *testing.T) {
	cfg := subagents.AgentConfig{Name: "implement", Description: "x", Tools: []string{"read_file", ConsultAdvisorToolName}, Prompt: "x"}
	tool, _ := newTestAgentTool(t, []subagents.AgentConfig{cfg}, nil, false)
	tool.AdvisorAdapter = &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("advice")}}}
	tool.AdvisorModel = "advisor-1"

	child := tool.buildChildRegistry(&cfg)
	if _, ok := child.Get(agentToolName); ok {
		t.Fatal("child registry must still exclude Agent recursion")
	}
	got, ok := child.Get(ConsultAdvisorToolName)
	if !ok {
		t.Fatal("child registry should include consult_advisor when advisor is configured")
	}
	if _, ok := got.(*ConsultAdvisorTool); !ok {
		t.Fatalf("consult tool type = %T", got)
	}
}
