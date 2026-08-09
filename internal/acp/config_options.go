package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/agentruntime"
)

// Session config option ids, referenced by both sessionConfigOptions (the
// advertised set) and SetSessionConfigOption (the dispatch switch) — kept
// as constants so the two can't drift out of sync, same pattern as
// authMethodOpenAIChatGPT/authMethodGitHubCopilot in auth.go.
const (
	configIdEffort  coderacp.SessionConfigId = "effort"
	configIdAdvisor coderacp.SessionConfigId = "advisor"
)

// effortWireOption pairs one agentruntime.EffortLevels entry with the
// wire-facing value id and display strings a client renders in its
// dropdown. "default" is the wire id for yottacode's internal ""
// (provider default, no reasoning override) — an empty string is an
// awkward value id for a client-rendered option list.
type effortWireOption struct {
	id, name, level, desc string
}

var effortWireOptions = []effortWireOption{
	{id: "default", name: "Default", level: "", desc: "Provider default — no reasoning override"},
	{id: "low", name: "Low", level: "low", desc: "Minimal reasoning, fastest"},
	{id: "medium", name: "Medium", level: "medium", desc: "Balanced"},
	{id: "high", name: "High", level: "high", desc: "Maximum reasoning, slowest"},
}

func effortLevelToWireID(level string) coderacp.SessionConfigValueId {
	for _, e := range effortWireOptions {
		if e.level == level {
			return coderacp.SessionConfigValueId(e.id)
		}
	}
	return coderacp.SessionConfigValueId("default")
}

func effortWireIDToLevel(id coderacp.SessionConfigValueId) (string, bool) {
	for _, e := range effortWireOptions {
		if e.id == string(id) {
			return e.level, true
		}
	}
	return "", false
}

// sessionConfigOptions builds the current, full set of ACP session config
// options for rt: reasoning effort (always advertised — every session has
// one, even if it's the empty/provider-default value) and advisor/
// implementer routing (only when a pair is actually configured for this
// session, so a client never sees a toggle that can only ever error out
// — mirrors internal/tui/cmd_router.go's own "/advisor on" guidance to
// configure a pair first). Shared by NewSession/LoadSession's initial
// ConfigOptions and SetSessionConfigOption's response, the same way
// sessionModeState is shared for modes.
func sessionConfigOptions(rt *agentruntime.Runtime) []coderacp.SessionConfigOption {
	effortOptions := make(coderacp.SessionConfigSelectOptionsUngrouped, 0, len(effortWireOptions))
	for _, e := range effortWireOptions {
		desc := e.desc
		effortOptions = append(effortOptions, coderacp.SessionConfigSelectOption{
			Name:        e.name,
			Value:       coderacp.SessionConfigValueId(e.id),
			Description: &desc,
		})
	}
	thoughtLevel := coderacp.SessionConfigOptionCategoryThoughtLevel
	effortDesc := "How much reasoning effort the model spends per turn."
	options := []coderacp.SessionConfigOption{
		{Select: &coderacp.SessionConfigOptionSelect{
			Id:           configIdEffort,
			Name:         "Reasoning effort",
			Description:  &effortDesc,
			Category:     &thoughtLevel,
			CurrentValue: effortLevelToWireID(rt.ChatOptions.ReasoningEffort),
			Options:      coderacp.SessionConfigSelectOptions{Ungrouped: &effortOptions},
			Type:         "select",
		}},
	}
	if rt.RouterAdapters != nil {
		advisorDesc := "Route subagent dispatch and summarization through the configured advisor/implementer model pair."
		options = append(options, coderacp.SessionConfigOption{Boolean: &coderacp.SessionConfigOptionBoolean{
			Id:           configIdAdvisor,
			Name:         "Advisor routing",
			Description:  &advisorDesc,
			CurrentValue: rt.RoutingAuto,
			Type:         "boolean",
		}})
	}
	return options
}

// SetSessionConfigOption dispatches by ConfigId, extracted from whichever
// of the request's Boolean/ValueId variants is set. Mutates the same
// rt.Adapter/rt.Cfg.Adapter/rt.AgentTool fields a live Prompt call reads
// throughout a turn — claimTurn/releaseTurn (the same guard
// acpSession.prompt uses to reject a second concurrent session/prompt,
// see session.go) keeps this from racing an in-flight turn, since neither
// of those fields is otherwise synchronized.
func (s *Server) SetSessionConfigOption(_ context.Context, params coderacp.SetSessionConfigOptionRequest) (coderacp.SetSessionConfigOptionResponse, error) {
	var configID coderacp.SessionConfigId
	var sessionID coderacp.SessionId
	switch {
	case params.ValueId != nil:
		configID, sessionID = params.ValueId.ConfigId, params.ValueId.SessionId
	case params.Boolean != nil:
		configID, sessionID = params.Boolean.ConfigId, params.Boolean.SessionId
	default:
		return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "missing value"})
	}

	sess, ok := s.session(string(sessionID))
	if !ok {
		return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown session id"})
	}
	if !sess.claimTurn() {
		return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "a turn is already in progress for this session"})
	}
	defer sess.releaseTurn()

	switch configID {
	case configIdEffort:
		if params.ValueId == nil {
			return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "effort expects a select value, not a boolean"})
		}
		level, ok := effortWireIDToLevel(params.ValueId.Value)
		if !ok {
			return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown effort value: " + string(params.ValueId.Value)})
		}
		if err := agentruntime.RebuildAdapterForEffort(sess.rt, level); err != nil {
			return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
	case configIdAdvisor:
		if params.Boolean == nil {
			return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "advisor expects a boolean value, not a select"})
		}
		if err := agentruntime.SetAdvisorRouting(sess.rt, params.Boolean.Value); err != nil {
			return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
	default:
		return coderacp.SetSessionConfigOptionResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown config option id: " + string(configID)})
	}

	return coderacp.SetSessionConfigOptionResponse{ConfigOptions: sessionConfigOptions(sess.rt)}, nil
}
