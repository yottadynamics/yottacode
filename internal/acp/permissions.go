package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// agent.Turn's internal loop already evaluates cfg.Permissions per tool
// call and only emits ApprovalNeeded for the Ask/Default outcomes
// (Allow emits ApprovalAuto instead, no round trip) — confirmed against
// internal/agent/loop.go's executeToolCallImpl. This bridge therefore
// only needs to translate ApprovalNeeded/PathTrustElevationNeeded into
// session/request_permission and map the client's chosen
// PermissionOptionKind back to the right agent.Decision; it does not
// re-implement permission evaluation.

// requestToolPermission handles agent.ApprovalNeeded: asks the client to
// approve or deny a tool call, always offering reject_once (clients
// match on kind, never a hardcoded optionId — see
// roadmap/acp-adapter.md §Permissions).
func requestToolPermission(ctx context.Context, conn *coderacp.AgentSideConnection, sessionID string, tracker *toolCallTracker, e agent.ApprovalNeeded) agent.Decision {
	id := tracker.idFor(e.ToolName, e.ArgsJSON)
	resp, err := conn.RequestPermission(ctx, coderacp.RequestPermissionRequest{
		SessionId: coderacp.SessionId(sessionID),
		ToolCall: coderacp.ToolCallUpdate{
			ToolCallId: id,
			Title:      coderacp.Ptr(e.Preview),
			RawInput:   e.ArgsJSON,
		},
		Options: []coderacp.PermissionOption{
			{Kind: coderacp.PermissionOptionKindAllowOnce, Name: "Allow once", OptionId: "allow_once"},
			{Kind: coderacp.PermissionOptionKindAllowAlways, Name: "Allow always", OptionId: "allow_always"},
			{Kind: coderacp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject_once"},
			{Kind: coderacp.PermissionOptionKindRejectAlways, Name: "Reject always", OptionId: "reject_always"},
		},
	})
	if err != nil {
		// Treat a transport-level failure as a deny — never silently
		// proceed with a tool call nobody actually approved.
		return agent.Deny
	}
	return decisionFromOutcome(resp.Outcome)
}

// requestPathElevation handles agent.PathTrustElevationNeeded — a
// second, distinct approval-round-trip variant from ApprovalNeeded (see
// roadmap/acp-adapter.md's Event mapping section). Reuses the same
// session/request_permission RPC; the valid agent.Decision values differ
// (PathAllowOnce/PathTrustSession/Deny, not AllowOnce/AllowAlways/Deny/
// DenyAlways).
func requestPathElevation(ctx context.Context, conn *coderacp.AgentSideConnection, sessionID string, e agent.PathTrustElevationNeeded) agent.Decision {
	resp, err := conn.RequestPermission(ctx, coderacp.RequestPermissionRequest{
		SessionId: coderacp.SessionId(sessionID),
		ToolCall: coderacp.ToolCallUpdate{
			// PathTrustElevationNeeded has no correlated tool_call id of
			// its own (it fires before the tool's normal approval path,
			// gating a path outside the session's trusted roots) — a
			// session-scoped synthetic id is enough since no ToolStart/
			// ToolResult ever references it.
			ToolCallId: coderacp.ToolCallId("path-trust-" + e.Path),
			Title:      coderacp.Ptr("Allow access outside the trusted workspace: " + e.Path),
			RawInput:   e.ArgsJSON,
		},
		Options: []coderacp.PermissionOption{
			{Kind: coderacp.PermissionOptionKindAllowOnce, Name: "Allow once", OptionId: "path_allow_once"},
			{Kind: coderacp.PermissionOptionKindAllowAlways, Name: "Trust for this session", OptionId: "path_trust_session"},
			{Kind: coderacp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject_once"},
		},
	})
	if err != nil {
		return agent.Deny
	}
	return decisionFromPathOutcome(resp.Outcome)
}

// requestUserQuestion handles agent.QuestionNeeded. ACP v1 has no
// client-facing primitive for it: session/request_permission returns a
// single OptionId from a flat list, which can't express multiple
// questions, multi_select, or free text. A per-question sequential
// approximation (one request_permission call per question, dropping
// multi_select/free-text fidelity) was considered and rejected — a
// lossy mapping that silently narrows what the model asked for is
// worse than a clear failure the model can react to. So this always
// fails closed, the same posture internal/oneshot takes when no
// recommended:true default is available: cancel the whole call and
// let the model fall back to a prose question in its reply. Revisit
// if/when the ACP spec grows a real elicitation method.
func requestUserQuestion(e agent.QuestionNeeded) agent.Decision {
	if e.Reply != nil {
		e.Reply.Cancelled = true
	}
	return agent.Deny
}

func decisionFromOutcome(outcome coderacp.RequestPermissionOutcome) agent.Decision {
	if outcome.Selected == nil {
		// Cancelled (or a malformed response with neither variant set):
		// deny. If this came from a session/cancel race, the turn's
		// context is being cancelled through the normal path anyway.
		return agent.Deny
	}
	switch outcome.Selected.OptionId {
	case "allow_once":
		return agent.AllowOnce
	case "allow_always":
		return agent.AllowAlways
	case "reject_always":
		return agent.DenyAlways
	default: // "reject_once" and anything unrecognized
		return agent.Deny
	}
}

func decisionFromPathOutcome(outcome coderacp.RequestPermissionOutcome) agent.Decision {
	if outcome.Selected == nil {
		return agent.Deny
	}
	switch outcome.Selected.OptionId {
	case "path_allow_once":
		return agent.PathAllowOnce
	case "path_trust_session":
		return agent.PathTrustSession
	default:
		return agent.Deny
	}
}
