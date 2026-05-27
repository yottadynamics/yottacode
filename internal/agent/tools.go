package agent

import (
	"context"
	"sync"

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

// MultimodalResult carries the output of a multimodal tool execution —
// text content plus optional image blocks.
type MultimodalResult struct {
	Content string
	Images  []adapter.ImageBlock
}

// MultimodalTool is an optional interface for tools that can produce
// image content alongside text output. The agent loop prefers
// ExecuteMultimodal over Execute when both are available; images are
// attached to the tool-result message so the model can see them.
type MultimodalTool interface {
	ExecuteMultimodal(ctx context.Context, argsJSON string) (MultimodalResult, error)
}

// Mutator is the optional capability marker for tools that modify files
// on disk. The checkpoint subsystem queries this before tool.Execute to
// capture pre-images so /checkpoints can restore. PathsToSnapshot
// returns absolute paths the tool intends to touch, derived from
// argsJSON. Returning extra paths is harmless (snapshots are
// content-addressed and dedup); returning too few breaks restore.
//
// Tools that mutate files via opaque side effects (e.g. run_bash) do
// NOT implement this — those mutations are intentionally untracked,
// mirroring Claude Code /rewind. Surface the limitation in user-facing
// docs / picker footer.
type Mutator interface {
	PathsToSnapshot(cwd, argsJSON string) []string
}

// ToolPathsToSnapshot exposes the Mutator capability to callers in
// other packages (e.g. the agent loop's checkpoint hook) without
// forcing them to import nothing-vs-something interface assertions.
// Returns nil when the tool isn't a Mutator.
func ToolPathsToSnapshot(t Tool, cwd, argsJSON string) []string {
	m, ok := t.(Mutator)
	if !ok {
		return nil
	}
	return m.PathsToSnapshot(cwd, argsJSON)
}

// Registry owns the set of tools exposed to a given agent run.
//
// Concurrency: the agent loop's goroutine calls Get to dispatch each
// tool invocation while the TUI goroutine may concurrently mutate the
// set (the /mcp restart path calls Deregister followed by Register on
// the fresh client's tool catalog). A concurrent map read+write is a
// Go runtime panic — not just inconsistent state — so every entry
// point takes the mutex. Lock granularity is the whole map: the
// expected mid-session mutation rate is so low (slash commands, not
// per-turn) that fine-grained locking would only add complexity for
// no measurable win.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// Deregister removes a tool by name. Returns true when a tool was
// actually removed, false when no such name was registered. Used by
// the MCP /mcp restart path to drop stale tools before re-registering
// the post-restart generation — without it, tools that disappear
// after a server restart would linger in the registry and surface as
// "missing client" errors on first invocation.
//
// Safe to call mid-session: tools currently executing aren't affected
// (they hold their own receiver), only future Get() lookups miss the
// removed name.
func (r *Registry) Deregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	return true
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	return t, ok
}

// Tools returns every registered Tool. The order is non-deterministic
// (map iteration). Subagent registry construction uses this to clone
// the parent's toolset while applying an allowlist filter — see
// internal/agent/agent_tool.go. Read-only on the registry: callers
// must not mutate the returned tools.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
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
