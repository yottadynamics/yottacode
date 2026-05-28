package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/skills"
)

// SkillToolName is the schema-visible name of the skill-invocation
// tool. Capital-S mirrors Claude Code's `Skill` surface. The name is
// referenced from the TUI / docs and slash-dispatch, so it lives as a
// const here.
const SkillToolName = "Skill"

// SkillTool is the model-facing entry point for the Agent Skills
// surface. The LLM picks a skill by name from the description-matched
// list it sees in the system prompt, calls `Skill(skill="<name>")`,
// and receives the skill's full body as the tool result. The model
// then continues the turn with the skill's guidance loaded.
//
// Skills are loaded once at session start (built-in + user + project,
// project shadows user shadows built-in) and the slice is stable for
// the session. A `/skills` reload command is a v1.1 follow-up.
type SkillTool struct {
	// All is the full resolved set loaded at session start (built-in +
	// user + project). Never mutated after construction; the TUI's
	// /skills picker uses this as the universe to render rows against.
	All []skills.Skill

	// enabled tracks which entries from All are exposed to the model
	// in the current session. Nil means "all enabled" — the default
	// for fresh sessions. Mutated by SetEnabled (driven by /skills).
	enabled map[string]bool
}

// SetEnabled installs the per-session enablement set. Passing nil
// resets to "all enabled". Names that aren't in All are ignored. The
// SkillTool's Description() reads through this filter so the model
// only sees the active skills on each turn.
func (t *SkillTool) SetEnabled(names map[string]bool) {
	if names == nil {
		t.enabled = nil
		return
	}
	out := make(map[string]bool, len(names))
	for n, on := range names {
		if on {
			out[n] = true
		}
	}
	t.enabled = out
}

// IsEnabled reports whether the named skill is currently exposed. A
// nil enablement map means everything is enabled (the default).
func (t *SkillTool) IsEnabled(name string) bool {
	if t.enabled == nil {
		return true
	}
	return t.enabled[name]
}

// Enable marks the named skill as exposed without disturbing the
// rest of the enablement set. Used by install paths so a
// just-installed skill is immediately visible — installing something
// strongly implies "I want to use this," and forcing a second trip
// through the picker for a checkbox was its own usability bug.
func (t *SkillTool) Enable(name string) {
	if t.enabled == nil {
		// Nil meant "all enabled" — adding to a nil map would be a
		// no-op against the "everything is on" baseline, so we
		// switch into an explicit map to make the addition stick.
		t.enabled = map[string]bool{}
	}
	t.enabled[name] = true
}

// Active returns the subset of All currently enabled, in input order.
// Used by callers that need to recompute prompt sections or slash
// dispatch around the active set.
func (t *SkillTool) Active() []skills.Skill {
	if t.enabled == nil {
		out := make([]skills.Skill, len(t.All))
		copy(out, t.All)
		return out
	}
	out := make([]skills.Skill, 0, len(t.All))
	for _, sk := range t.All {
		if t.enabled[sk.Name] {
			out = append(out, sk)
		}
	}
	return out
}

func (t *SkillTool) Name() string { return SkillToolName }

// Description lists every skill's name + description so the model can
// pick by keyword match. Spec calls this the "metadata" tier of
// progressive disclosure — small, always loaded; bodies stay out
// until activation.
func (t *SkillTool) Description() string {
	var b strings.Builder
	b.WriteString("Execute a reusable capability playbook (Agent Skill). Each skill is a short markdown body describing what to do and when. When you call this tool, the skill's body is returned as the tool result for you to apply in the current turn.\n\n")
	b.WriteString("Use this tool when a user request matches a skill's described scope. Do NOT call a skill that is not in the list below.\n\n")
	b.WriteString("Available skills:\n")
	sorted := append([]skills.Skill(nil), t.Active()...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, sk := range sorted {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
}

func (t *SkillTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "The name of the skill to load. Must match one of the names listed in the tool description exactly.",
			},
		},
		"required": []string{"skill"},
	}
}

// RequiresApproval is always false — loading a playbook into the
// model's context is just text injection. Any mutating action the
// skill's body recommends still flows through the *underlying* tool's
// own approval gate (run_bash, write_file, etc.).
func (t *SkillTool) RequiresApproval(string) bool { return false }

// ParallelSafe so a model can grab multiple complementary skills in
// one assistant message (e.g. `diagnose` + `verification-before-completion`
// for a bug fix). Each call is a pure read of an embedded string;
// concurrent calls have no shared mutable state.
func (t *SkillTool) ParallelSafe(string) bool { return true }

func (t *SkillTool) PreviewCall(argsJSON string) string {
	a := parseSkillArgs(argsJSON)
	return fmt.Sprintf("Skill[%s]", a.Skill)
}

type skillArgs struct {
	Skill string `json:"skill"`
}

func parseSkillArgs(argsJSON string) skillArgs {
	var a skillArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a
}

// Execute returns the named skill's body so the model can fold it
// into the next iteration. An unknown skill name returns a
// recoverable error string the model can react to (suggest a near
// match, ask the user, or give up) — same pattern AgentTool uses for
// unknown subagent_type.
func (t *SkillTool) Execute(_ context.Context, argsJSON string) (string, error) {
	a := parseSkillArgs(argsJSON)
	if strings.TrimSpace(a.Skill) == "" {
		return "", fmt.Errorf("Skill: skill is required")
	}
	sk := skills.Find(t.Active(), a.Skill)
	if sk == nil {
		return t.unknownSkillError(a.Skill), nil
	}
	// The body is the spec's "body tier" of progressive disclosure —
	// the full playbook the model uses to drive its next reply.
	// Resources (scripts/, references/, assets/) are referenced by
	// relative path in the body and loaded on demand through
	// read_file / run_bash.
	return sk.Body, nil
}

func (t *SkillTool) unknownSkillError(name string) string {
	available := make([]string, 0, len(t.Active()))
	for _, sk := range t.Active() {
		available = append(available, sk.Name)
	}
	sort.Strings(available)
	return fmt.Sprintf("error: unknown skill %q (available: %s)", name, strings.Join(available, ", "))
}
