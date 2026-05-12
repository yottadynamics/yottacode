package agent

import (
	"context"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// Tool is one capability the agent can invoke. Execute receives the raw JSON
// arguments the model emitted; tools parse them internally so the registry
// stays schema-agnostic.
//
// RequiresApproval takes the argsJSON because some tools (e.g. the unified
// git tool) decide policy based on the specific subcommand: `git status`
// auto-executes, `git push --force` prompts. Tools that don't care about
// args may ignore the parameter.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	RequiresApproval(argsJSON string) bool
	PreviewCall(argsJSON string) string
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// ParallelSafeTool is an optional capability marker for tools that can run
// concurrently with other read-only tool calls from the same assistant
// message. Keep this narrow and explicit: a false negative only costs some
// latency, but a false positive can create hard-to-debug races.
type ParallelSafeTool interface {
	ParallelSafe(argsJSON string) bool
}

func toolParallelSafe(t Tool, argsJSON string) bool {
	ps, ok := t.(ParallelSafeTool)
	return ok && ps.ParallelSafe(argsJSON)
}

// Registry owns the set of tools exposed to a given agent run.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Tools returns every registered Tool. The order is non-deterministic
// (map iteration). Subagent registry construction uses this to clone
// the parent's toolset while applying an allowlist filter — see
// internal/agent/agent_tool.go. Read-only on the registry: callers
// must not mutate the returned tools.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Names returns the set of registered tool names. Useful for callers
// that need to validate references (e.g. subagent allowlists) without
// caring about the Tool values themselves.
func (r *Registry) Names() map[string]bool {
	out := make(map[string]bool, len(r.tools))
	for name := range r.tools {
		out[name] = true
	}
	return out
}

// AsAdapterTools converts the registry into the schema shape the adapter
// advertises to the model. Equivalent to AsAdapterToolsFiltered(nil) —
// every registered tool is exposed.
func (r *Registry) AsAdapterTools() []adapter.Tool {
	return r.AsAdapterToolsFiltered(nil)
}

// AsAdapterToolsFiltered is the gated variant: when filter is non-nil
// and returns false for a tool name, that tool is omitted from the
// advertised schema. Used by the loop to hide `exit_plan_mode` outside
// of plan mode — without the filter the model could synthesize the
// call out of context and confuse the user with an approval card for
// a plan that doesn't exist. Pure read-side filter; the registry's
// own map is unchanged so Get() still resolves the tool when
// (legitimately) called.
func (r *Registry) AsAdapterToolsFiltered(filter func(name string) bool) []adapter.Tool {
	out := make([]adapter.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if filter != nil && !filter(t.Name()) {
			continue
		}
		out = append(out, adapter.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}
