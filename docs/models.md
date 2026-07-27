# Configuring models

A model id tells yottacode which model to send each turn to. Model names are provider-specific.

## Set the startup model

Environment variable:

```bash
export YOTTACODE_MODEL=<your-model-id>
```

Flag:

```bash
yottacode --model <your-model-id> --base-url https://api.openai.com/v1 --api-key sk-...
```

Config file:

```toml
[active]
provider      = "openai"
default_model = "<your-model-id>"
```

## Switch models in the TUI

```text
/model <your-model-id>
```

This changes the active model for the current session and rebuilds the adapter. It does not necessarily rewrite your shell environment.

## Manage default models from the CLI

```bash
yottacode model list
yottacode model list --all
yottacode model use <your-model-id>
yottacode model fetch
yottacode model fetch openai
```

`model use` updates the configured active `default_model`.

## Fetch live models

```bash
yottacode model fetch openai
```

This calls the provider `/models` endpoint and prints the merged model list. It is useful when checking auth, endpoint shape, or whether a newly released model is visible to your account.

## Reasoning models

Some models expose reasoning streams or summaries:

| Provider/model family | Behavior |
|---|---|
| OpenAI `o1*`, `o3*`, `o4*`, `gpt-5*` | Routed to Responses API when appropriate |
| `openai-auth` account models | Use the ChatGPT-authenticated backend and surface reasoning summaries where available |
| `copilot` account models | Use the GitHub Copilot backend; available models depend on subscription tier (Free/Pro/Pro+) |
| xAI Grok reasoning models | Reasoning content is surfaced when present |
| Ollama thinking models | Reasoning fields are surfaced when the OpenAI shim provides them |
| Standard chat models | Stream final content only |

Use `--reasoning-effort low|medium|high` when the selected provider/model supports it.

## Hosted tools by model/provider

Hosted provider tools depend on provider support, not just the model name.

- OpenAI: `web_search` default-on; `code_interpreter` optional
- xAI: `web_search` and `x_search` default-on; `code_interpreter` optional
- Ollama/custom OpenAI-compatible: no hosted provider tools; local yottacode tools still work

## Cache-safe task routing

Routing lets yottacode assign work to two explicit roles:

- **advisor** — the reasoning/planning/design model.
- **implementer** — the fast coding model used for auto-mode work,
  delegated subagents, and summarization.

It is opt-in, configurable from the TUI with **`/router`** (recommended —
see below) or by hand via the `[router]` block in
`~/.yottacode/config.toml`:

```toml
[router]
  mode              = "auto"                          # off | manual | auto
  advisor_model     = "anthropic:claude-opus-4-6"
  implementer_model = "anthropic:claude-haiku-4-5"
```

`advisor_model` / `implementer_model` use the same `"<provider>"` or
`"<provider>:<model>"` grammar as the multi-provider router's
`candidates`. Both are required when `mode` is not `off`. Provider names
refer to your `[[providers]]` blocks, and the model must be listed in
that provider's `models` (typos are rejected at load time). Legacy
`smart_model` and `fast_model` still load as aliases for advisor and
implementer, but new writes use the role-named keys. Reasoning effort is
session-wide: set it with `/effort` or `--reasoning-effort`, not with
per-role router fields.

### Failover chains

Each slot can be a **failover chain** instead of a single model — give it
the plural form with a primary followed by fallbacks:

```toml
[router]
  mode               = "auto"
  implementer_model  = "anthropic:claude-haiku-4-5"                   # single is fine
  advisor_models     = ["anthropic:claude-opus-4-6", "openai:gpt-4o"] # primary, then fallback
  health_window_seconds    = 60
  health_failure_threshold = 3
```

The first entry is always the primary — chains dispatch in written
order (the `policy` knob orders the multi-provider candidates router
only, not these slots). On an error before any output the call falls
through to the next entry, sharing the health knobs with the
multi-provider router (a flapping provider is skipped until it
recovers). There is no router-level timeout — a hung call is bounded
by the underlying adapter/provider timeouts, not by the chain. So an
advisor consultation, subagent, or summarization call survives a
role-model outage instead of failing. A slot uses the singular **or**
the plural form, not both.

When a fallover happens it's surfaced loudly, the same way main-thread
fallbacks already are — a warm-yellow line tagged with where it occurred:

```text
↻ fallback [consult_advisor]: anthropic/claude-opus-4-6 → openai/gpt-4o [fallback-chain]: <reason>
↻ fallback [summarize]: anthropic/claude-haiku-4-5 → openai/gpt-4o-mini: <reason>
```

The status chip and subagent cards still show the primary. The `/router`
picker has an **Advisor fallback** row: Enter sets it (a one-model
fallback), `d` clears it. Chains longer than two stay in `config.toml` —
the picker shows `(+N more)` and preserves them.

The **implementer slot has no fallback row** in the picker. An
`implementer_models` chain set by hand in `config.toml` is still honored;
it is just not surfaced in the picker.

### Configuring from the TUI: `/router`

You don't have to edit the file by hand. **`/router`** opens a picker
with rows — Routing, Advisor model, Implementer, Advisor fallback — that
all act in place (the picker stays open). Toggle the Routing row on/off,
and open the model rows to pick from your configured models (the embedded
catalog plus any `providers.models`). You can enable routing first and
choose the models below — routing turns on once both are set — or pick
the models and then toggle on. Selections persist to `config.toml` and
apply live; picking a catalog model also records it in that provider's
`providers.models` so the write validates. `/router on` and `/router off`
are quick shortcuts for the toggle.

**Configuring the advisor model also switches your active model.** When
you set (or change) the **Advisor model** and close the picker, your main
conversation switches to that model — the advisor model is your primary
reasoning model, so this keeps the two in sync (the same as running
`/model <advisor>`). Closing without changing the advisor model leaves
your active model untouched.

While routing is active (`auto`) the status bar's primary segment stays the
active top-level model, with the routing mode shown inline beside it, e.g.
`● gpt-5.5 auto · ctx …`. It does **not** show `<advisor>:<implementer>` as a
pair, because that would look like the current turn is running on both models.
(`manual` mode likewise keeps the active model and adds `manual`; `off` shows
just the active model.)

### Why this saves money (and never costs more)

In an agentic loop the dominant cost is **re-sending the full context**
(system prompt + files + history) on every turn. Prompt caching makes
repeat turns on the *same* model cheap — cache reads are a fraction of
the input price. Switching the **main-thread** model mid-conversation
would throw that cache away on *both* models and cost *more*, so yottacode
only changes the active model at explicit session/mode boundaries such as
startup, `/plan`, `/auto`, `/model`, or a `/router` picker selection.

Routing also targets contexts that **never shared the main thread's
cache** in the first place:

- **Subagents** each build a fresh, isolated context window.
- **Advisor consultations** are isolated no-tool calls.
- **Summarization / compaction** is a single isolated call.

### Modes

| `mode` | Behavior |
|---|---|
| `off` (default) | Routing disabled. Everything runs on your active model. Fully backward compatible. |
| `manual` | Resolves `advisor_model` / `implementer_model`, but only routes a subagent when its definition declares an explicit `model:` (see [subagents.md](subagents.md)). Non-annotated agents inherit your active model, exactly as with routing off. |
| `auto` | Session startup and `/plan` use `advisor_model`. Permission auto mode (`Shift+Tab` or `--permission-mode auto`), **summarization / history compaction**, and **every delegated subagent** use `implementer_model`. Implementer-style subagents can call `consult_advisor` for bounded design/debugging help. An explicit `model:` on an agent definition overrides this. |

The split is deliberate: planning and design should happen on the
advisor; routine implementation and isolated child work can run on the
faster implementer. If the implementer gets stuck, `consult_advisor` gives
it a bounded no-tools path to ask the advisor without recursively spawning
another agent. That tool is available to implementer subagents and to the
top-level conversation while auto mode is driving the main session with the
implementer; advisor-led sessions and plan mode do not expose it. Your
reasoning-effort selection remains global via `/effort`, so roles do not fight
over hidden effort settings.

### Seeing what ran where

The model a subagent ran on is shown in the `/subagents` picker and on
each subagent's completion card (`… · on claude-haiku-4-5`), so you can
confirm at a glance which role handled each task.

> Note: yottacode does not yet aggregate per-model token totals or cost
> across a session — token figures shown are per-subagent estimates.

### Relationship to the multi-provider router

The same `[router]` block also hosts the **multi-provider failover
router** (`enabled`, `candidates`, `policy`, health knobs), which
dispatches each *main-thread* turn across candidates with fallback. That
is a separate, orthogonal feature: failover is about resilience across
providers; task routing (`mode` / `advisor_model` / `implementer_model`) is about
assigning isolated work to roles. They can be configured independently.

## No silent fallback

If the model or base URL is missing, yottacode exits with a clear error. It does not silently default to localhost or a paid cloud provider.

## Choosing a model

Practical starting points:

- Local/privacy-first: Ollama with Qwen, Llama, or DeepSeek models
- General coding: a strong OpenAI-compatible coding model
- Deep planning: a reasoning model, with higher latency/cost
- Scripting/CI: cheaper fast model plus low `--max-iterations`

Use `/doctor` or `yottacode doctor` when a model is configured but not visible to the provider.
