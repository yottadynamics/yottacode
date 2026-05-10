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

// AsAdapterTools converts the registry into the schema shape the adapter
// advertises to the model.
func (r *Registry) AsAdapterTools() []adapter.Tool {
	out := make([]adapter.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, adapter.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}
