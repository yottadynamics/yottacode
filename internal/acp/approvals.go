package acp

import (
	"context"
	"fmt"

	coderacp "github.com/coder/acp-go-sdk"
)

type pendingApproval struct {
	id         string
	generation uint64
	request    coderacp.RequestPermissionRequest
	result     chan coderacp.RequestPermissionResponse
}

func (s *acpSession) requestPermission(ctx context.Context, req coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	s.mu.Lock()
	s.approvalSeq++
	id := fmt.Sprintf("approval_%d", s.approvalSeq)
	if req.ToolCall.ToolCallId == "" {
		req.ToolCall.ToolCallId = coderacp.ToolCallId(id)
	}
	pending := &pendingApproval{id: id, generation: s.controllerGeneration, request: req, result: make(chan coderacp.RequestPermissionResponse, 1)}
	if s.pendingApprovals == nil {
		s.pendingApprovals = make(map[string]*pendingApproval)
	}
	s.pendingApprovals[id] = pending
	target := s.controller
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pendingApprovals, id)
		s.mu.Unlock()
	}()
	if target != nil {
		go func() {
			resp, err := target.RequestPermission(ctx, req)
			if err == nil {
				select {
				case pending.result <- resp:
				default:
				}
			}
		}()
	}
	select {
	case resp := <-pending.result:
		return resp, nil
	case <-ctx.Done():
		return coderacp.RequestPermissionResponse{}, ctx.Err()
	}
}

func (s *acpSession) pendingApprovalList() []coderacp.RequestPermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]coderacp.RequestPermissionRequest, 0, len(s.pendingApprovals))
	for _, pending := range s.pendingApprovals {
		out = append(out, pending.request)
	}
	return out
}

func (s *acpSession) resolveApproval(id string, option coderacp.PermissionOptionId) error {
	s.mu.Lock()
	pending := s.pendingApprovals[id]
	generation := s.controllerGeneration
	s.mu.Unlock()
	if pending == nil {
		return coderacp.NewInvalidParams(map[string]any{"error": "unknown approval"})
	}
	if pending.generation == generation {
		// Current and replacement controllers can resolve a parked request;
		// stale connections are rejected by the connection-agent generation.
	}
	select {
	case pending.result <- coderacp.RequestPermissionResponse{Outcome: coderacp.RequestPermissionOutcome{Selected: &coderacp.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: option}}}:
		return nil
	default:
		return coderacp.NewInvalidParams(map[string]any{"error": "approval already resolved"})
	}
}
