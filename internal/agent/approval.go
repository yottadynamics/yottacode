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
	// PathAllowOnce is the inline path-trust elevation answer for
	// "[1] Allow once" — let this exact write proceed by adding the
	// file's path to the session-scoped allow list. Future writes to
	// other files outside cwd still prompt. Session-only, never
	// persisted to ~/.yottacode/trusted-roots.json.
	PathAllowOnce
	// PathTrustSession is the inline path-trust elevation answer for
	// "[2] Trust this directory for the session" — adds the file's
	// parent directory to the session-scoped allow list so every
	// future write under that directory also succeeds. Session-only.
	PathTrustSession
	// DenyAlways refuses this call (like Deny) AND asks the loop to
	// derive a block pattern from it (via permissions.DeriveDenyRule) and
	// append it to the deny[] list in permissions.local.json, so future
	// matching calls are refused without prompting. The mirror of
	// AllowAlways. Unlike AllowAlways it is offered even for dangerous or
	// compound commands — those are exactly the calls a user most wants to
	// block permanently. Scope is currently run_bash + git.
	DenyAlways
	// AllowSession permits this call and asks the loop to derive a
	// pattern from it (same permissions.DeriveAllowRule as AllowAlways)
	// but keeps the resulting rule in memory only, via
	// Permissions.AddSessionAllow — never written to
	// permissions.local.json. The grant lasts for the remainder of this
	// process and disappears on restart; it also can't be accidentally
	// erased (or leaked to a teammate) by a `/permissions` file edit and
	// reload. The middle ground between AllowOnce (this call only) and
	// AllowAlways (persisted forever). Offered under the same
	// derivability gate as AllowAlways.
	AllowSession
)
