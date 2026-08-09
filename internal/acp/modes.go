package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
)

// ACP session mode ids, matching roadmap/acp-adapter.md's mode-mapping
// table.
const (
	modeAsk       coderacp.SessionModeId = "ask"
	modeArchitect coderacp.SessionModeId = "architect"
	modeCode      coderacp.SessionModeId = "code"
	modeYolo      coderacp.SessionModeId = "yolo"
)

// sessionModes is the fixed AvailableModes list for every yottacode ACP
// session — same four modes regardless of session state.
func sessionModes() []coderacp.SessionMode {
	return []coderacp.SessionMode{
		{Id: modeAsk, Name: "Ask", Description: coderacp.Ptr("Request permission before making any changes")},
		{Id: modeArchitect, Name: "Architect", Description: coderacp.Ptr("Design and plan software systems without implementation")},
		{Id: modeCode, Name: "Code", Description: coderacp.Ptr("Write and modify code with full tool access")},
		{Id: modeYolo, Name: "Yolo", Description: coderacp.Ptr("Every tool auto-runs, no safety floor — DANGEROUS")},
	}
}

// currentModeID reports which of the four ids reflects rt's current
// state. yottacode's own model has Yolo as an orthogonal overlay that
// can sit alongside Plan or Auto or neither (see internal/tui/cmd_yolo.go),
// but ACP session modes are a single selected value, not a set of
// independent flags — v1 picks one to report, and Yolo takes precedence
// when active since it's the most permissive/dangerous state and the
// client should always see it clearly rather than have it hidden behind
// whichever named mode happens to be layered underneath.
func currentModeID(rt *agentruntime.Runtime) coderacp.SessionModeId {
	switch {
	case rt.YoloMode.IsActive():
		return modeYolo
	case rt.PlanMode.IsActive():
		return modeArchitect
	case rt.AutoMode.IsActive():
		return modeCode
	default:
		return modeAsk
	}
}

// sessionModeState bundles sessionModes() with the current selection —
// used by NewSession/LoadSession responses and by SetSessionMode's own
// response.
func sessionModeState(rt *agentruntime.Runtime) *coderacp.SessionModeState {
	return &coderacp.SessionModeState{
		AvailableModes: sessionModes(),
		CurrentModeId:  currentModeID(rt),
	}
}

// applyMode transitions rt's mode state to id. Ask/Architect/Code are
// mutually exclusive with each other AND, unlike yottacode's own
// TUI-internal orthogonal-overlay model, they also exit Yolo — ACP's
// session/set_mode is a single-select picker with no way for a client
// to express "stay in yolo AND switch to architect", so explicitly
// picking a named mode is the only signal a client has for "leave the
// dangerous overlay." Yolo itself stays additive (does not touch
// whatever Plan/Auto state was already there), matching
// internal/tui/cmd_yolo.go's enterYoloMode exactly — a client picks
// Yolo to layer it on, and picks a named mode to clear it again. One
// subtle consequence inherited from the TUI: if Yolo is layered on top
// of Architect, ACP reports the selected mode as Yolo while the agent
// loop still evaluates PlanModeGate before the yolo permission path, so
// write tools remain restricted to the active plan file until the client
// explicitly picks Ask or Code to leave Architect.
func applyMode(rt *agentruntime.Runtime, id coderacp.SessionModeId) {
	switch id {
	case modeArchitect:
		rt.AutoMode.Active.Store(false)
		rt.YoloMode.Active.Store(false)
		rt.PlanMode.PlanFile = ""
		applyPlanFileToWriteTools(rt.Registry, "")
		rt.PlanMode.Active.Store(true)
	case modeCode:
		rt.PlanMode.Active.Store(false)
		rt.PlanMode.PlanFile = ""
		applyPlanFileToWriteTools(rt.Registry, "")
		rt.YoloMode.Active.Store(false)
		rt.AutoMode.Active.Store(true)
	case modeYolo:
		rt.YoloMode.Active.Store(true)
	default: // modeAsk, and anything unrecognized
		rt.PlanMode.Active.Store(false)
		rt.PlanMode.PlanFile = ""
		applyPlanFileToWriteTools(rt.Registry, "")
		rt.AutoMode.Active.Store(false)
		rt.YoloMode.Active.Store(false)
	}
}

// applyPlanFileToWriteTools restricts write_file/edit_file/apply_diff to
// planFile only (or clears the restriction when planFile is ""),
// duplicated from internal/tui/cmd_plan.go since that function is
// private to the tui package. Kept in exact sync — see that file if this
// ever needs to change.
func applyPlanFileToWriteTools(reg *agent.Registry, planFile string) {
	if reg == nil {
		return
	}
	for _, name := range []string{"write_file", "edit_file", "apply_diff"} {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		switch tt := t.(type) {
		case *agent.WriteFileTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		case *agent.EditFileTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		case *agent.ApplyDiffTool:
			tt.WriteOpts.PlanModeAllowedFile = planFile
		}
	}
}

// SetSessionMode maps an ACP mode selection onto yottacode's mode
// states. The wire-level plumbing for this (the RPC itself) is provided
// by coder/acp-go-sdk; only the mapping table and the state transition
// are ours — see roadmap/acp-adapter.md §Permissions Mode mapping.
func (s *Server) SetSessionMode(_ context.Context, params coderacp.SetSessionModeRequest) (coderacp.SetSessionModeResponse, error) {
	sess, ok := s.session(string(params.SessionId))
	if !ok {
		return coderacp.SetSessionModeResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnActive {
		return coderacp.SetSessionModeResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "cannot change session mode while a turn is in progress"})
	}
	applyMode(sess.rt, params.ModeId)
	return coderacp.SetSessionModeResponse{}, nil
}
