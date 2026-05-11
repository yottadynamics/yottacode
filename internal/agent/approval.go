package agent

// Decision is the verdict the consumer sends back when the loop emits
// an ApprovalNeeded event. AllowAlways writes a derived rule to the
// project-local .yottacode/permissions.local.json via the
// permissions.Permissions value passed in LoopConfig.
type Decision int

const (
	// Deny refuses this single call and reports "denied by user" to
	// the model so it can recover.
	Deny Decision = iota
	// AllowOnce permits this single call. No persistence.
	AllowOnce
	// AllowAlways permits this call and asks the loop to derive a
	// pattern from it (via permissions.DeriveAllowRule) and append it
	// to permissions.local.json so future matching calls are silent.
	// The TUI suppresses this option for cases where derivation isn't
	// safe (compound shell commands, dangerous verbs).
	AllowAlways
	// SaveForLater is the plan-mode-specific "[L] approve and
	// implement later" decision. The loop refuses the tool call (so
	// Execute never runs) but returns a firm "end this turn" message
	// to the model instead of the generic denial / refinement hint.
	// Only meaningful for exit_plan_mode; other tools should never
	// receive this value, and the loop falls back to generic-denial
	// semantics if they do.
	SaveForLater
)
