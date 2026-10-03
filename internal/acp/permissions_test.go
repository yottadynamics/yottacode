package acp

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// requestUserQuestion always fails closed today — ACP's only
// interactive primitive (session/request_permission) can't express
// multiple questions, multi_select, or free text, and a lossy
// per-question approximation was rejected in favor of a clear
// failure the model can react to. See the function's own doc comment
// for the full rationale.
func TestRequestUserQuestion_AlwaysFailsClosed(t *testing.T) {
	reply := &agent.QuestionAnswer{}
	e := agent.QuestionNeeded{
		Questions: []agent.Question{{
			Header:   "Auth",
			Question: "How should we authenticate?",
			Options:  []agent.QuestionOption{{Label: "OAuth"}, {Label: "API key"}},
		}},
		Reply: reply,
	}

	got := requestUserQuestion(e)
	if got != agent.Deny {
		t.Errorf("requestUserQuestion() = %v, want agent.Deny", got)
	}
	if !reply.Cancelled {
		t.Errorf("expected Reply.Cancelled = true")
	}
}

func TestRequestUserQuestion_NilReplyDoesNotPanic(t *testing.T) {
	got := requestUserQuestion(agent.QuestionNeeded{Reply: nil})
	if got != agent.Deny {
		t.Errorf("requestUserQuestion() = %v, want agent.Deny", got)
	}
}
