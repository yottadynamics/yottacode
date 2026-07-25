package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

const ConsultAdvisorToolName = "consult_advisor"

// ConsultAdvisorTool lets an implementer subagent ask the configured advisor
// model for bounded design/debug guidance without recursively dispatching a
// child agent. The advisor call receives no tools, so it cannot mutate state or
// call back into consult_advisor.
type ConsultAdvisorTool struct {
	Advisor Streamer
	Model   string
	Timeout time.Duration
}

func (t *ConsultAdvisorTool) Name() string { return ConsultAdvisorToolName }

func (t *ConsultAdvisorTool) Description() string {
	return "Ask the configured advisor model for concise planning, design, or debugging guidance when you are stuck. Use from implementer subagents only; the advisor cannot call tools."
}

func (t *ConsultAdvisorTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "The specific design, planning, or debugging question for the advisor.",
			},
			"context": map[string]any{
				"type":        "string",
				"description": "Optional concise context: current approach, failing error, relevant files, or constraints.",
			},
		},
		"required":             []string{"question"},
		"additionalProperties": false,
	}
}

func (t *ConsultAdvisorTool) RequiresApproval(string) bool { return false }
func (t *ConsultAdvisorTool) PreviewCall(string) string    { return "consult_advisor" }

func (t *ConsultAdvisorTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	if t == nil || t.Advisor == nil {
		return "", errors.New("consult_advisor: no advisor model is configured")
	}
	var args struct {
		Question string `json:"question"`
		Context  string `json:"context"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		return "", errors.New("consult_advisor: question is required")
	}
	limit := t.Timeout
	if limit <= 0 {
		limit = 90 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	var user strings.Builder
	user.WriteString("Question:\n")
	user.WriteString(question)
	if c := strings.TrimSpace(args.Context); c != "" {
		user.WriteString("\n\nContext:\n")
		user.WriteString(c)
	}
	messages := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "You are the advisor model for yottacode. Give concise, practical design/debugging guidance to an implementer. Do not ask to run tools; state the recommended next step and key tradeoff."},
		{Role: adapter.RoleUser, Content: user.String()},
	}
	stream := t.Advisor.ChatStream(callCtx, messages, nil)
	var final strings.Builder
	for ev := range stream {
		switch ev.Kind {
		case adapter.EventTokenDelta:
			final.WriteString(ev.Token)
		case adapter.EventDone:
			if ev.Final != nil && strings.TrimSpace(ev.Final.Content) != "" {
				return strings.TrimSpace(ev.Final.Content), nil
			}
			if out := strings.TrimSpace(final.String()); out != "" {
				return out, nil
			}
		case adapter.EventErr:
			if ev.Err != nil {
				return "", ev.Err
			}
		}
	}
	if err := callCtx.Err(); err != nil {
		return "", err
	}
	if out := strings.TrimSpace(final.String()); out != "" {
		return out, nil
	}
	return "", errors.New("consult_advisor: advisor returned no answer")
}
