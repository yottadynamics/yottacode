package agentruntime

import (
	"sort"
	"testing"

	"github.com/yottadynamics/yottacode/internal/permissions"
)

// TestBuild_ApprovalGatedToolsHaveRuleTarget guards the approval modal's
// [A]lways / [S]ession keys: a tool that prompts but has no permission
// target can never be allowed by a saved rule, so its prompt can't be
// silenced. Every approval-gated tool must either map to a permission
// target (permissions.SupportsToolName) or be listed in noRuleTarget with
// the reason it is deliberately un-persistable. Adding a prompting tool
// without choosing one fails here.
//
// RequiresApproval is probed with empty args, so a tool that only prompts
// for some argument shapes is not covered; its targetFor case is still
// expected, this sweep just can't enumerate it.
func TestBuild_ApprovalGatedToolsHaveRuleTarget(t *testing.T) {
	noRuleTarget := map[string]string{
		"enter_plan_mode": "plan-boundary card, not a permission",
		"exit_plan_mode":  "plan-boundary card, not a permission",
		// Run or evaluate arbitrary code: a standing grant is exactly what
		// the auto-mode safety floor exists to prevent.
		"debug_start": "arbitrary code execution; per-call approval only",
		"debug_eval":  "arbitrary code execution; per-call approval only",
	}

	rt := mustBuild(t, newTestSpec(t))
	var missing []string
	for _, tool := range rt.Registry.Tools() {
		name := tool.Name()
		if !tool.RequiresApproval("{}") {
			continue
		}
		if _, ok := noRuleTarget[name]; ok || permissions.SupportsToolName(name) {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("approval-gated tools with no permission target (add a targetFor case or list in noRuleTarget): %v", missing)
	}
}
