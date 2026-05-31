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
- xAI: `web_search` default-on; `x_search` and `code_interpreter` optional
- Ollama/custom OpenAI-compatible: no hosted provider tools; local yottacode tools still work

## Cache-safe task routing

Routing lets yottacode run **isolated, throwaway work** (subagents and
history compaction) on a cheap **fast** model while your main
conversation stays on your chosen **smart** model. It is opt-in via the
`[router]` block in `~/.yottacode/config.toml`:

```toml
[router]
  mode        = "auto"                          # off | manual | auto
  fast_model  = "anthropic:claude-haiku-4-5"
  smart_model = "anthropic:claude-opus-4-6"
```

`fast_model` / `smart_model` use the same `"<provider>"` or
`"<provider>:<model>"` grammar as the multi-provider router's
`candidates`. Both are required when `mode` is not `off`. Provider names
refer to your `[[providers]]` blocks, and the model must be listed in
that provider's `models` (typos are rejected at load time).

### Why this saves money (and never costs more)

In an agentic loop the dominant cost is **re-sending the full context**
(system prompt + files + history) on every turn. Prompt caching makes
repeat turns on the *same* model cheap — cache reads are a fraction of
the input price. Switching the **main-thread** model mid-conversation
would throw that cache away on *both* models and cost *more*, so
yottacode never does it.

Routing only ever targets contexts that **never shared the main thread's
cache** in the first place:

- **Subagents** each build a fresh, isolated context window.
- **Summarization / compaction** is a single isolated call.

Running those on the fast model is a pure saving with zero cache churn.
Your interactive turns are untouched.

### Modes

| `mode` | Behavior |
|---|---|
| `off` (default) | Routing disabled. Everything runs on your active model. Fully backward compatible. |
| `manual` | Resolves `fast_model` / `smart_model`, but only routes a subagent when its definition declares an explicit `model:` (see [subagents.md](subagents.md)). Non-annotated agents inherit your active model, exactly as with routing off. |
| `auto` | Routes by the agent's nature: **read-only / search subagents** (the `Explore` and `Plan` built-ins) and **summarization** → `fast_model`; **everything else** (the `general-purpose` and `verification` built-ins, or any agent that can mutate/run) → `smart_model`. |

The `auto` heuristic is **deterministic and free** — it inspects each
agent's declared tool allowlist, with no extra model call to classify
the task. An agent restricted to read-only tools (read_file, grep, glob,
git read subcommands, etc.) routes to `fast_model`; an agent that can
mutate the workspace or run commands (`run_bash`, `write_file`, …)
routes to `smart_model`. An explicit `model:` on an agent definition
always wins over the heuristic. (Your **main conversation** is never
affected either way — only subagents and summarization.)

### Seeing what ran where

The model a subagent ran on is shown in the `/subagents` picker and on
each subagent's completion card (`… · on claude-haiku-4-5`), so you can
confirm at a glance that a search subagent used the fast model and a
heavier one used the smart model.

> Note: yottacode does not yet aggregate per-model token totals or cost
> across a session — token figures shown are per-subagent estimates.

### Relationship to the multi-provider router

The same `[router]` block also hosts the **multi-provider failover
router** (`enabled`, `candidates`, `policy`, health knobs), which
dispatches each *main-thread* turn across candidates with fallback. That
is a separate, orthogonal feature: failover is about resilience across
providers; task routing (`mode` / `fast_model` / `smart_model`) is about
spending less on isolated work. They can be configured independently.

## No silent fallback

If the model or base URL is missing, yottacode exits with a clear error. It does not silently default to localhost or a paid cloud provider.

## Choosing a model

Practical starting points:

- Local/privacy-first: Ollama with Qwen, Llama, or DeepSeek models
- General coding: a strong OpenAI-compatible coding model
- Deep planning: a reasoning model, with higher latency/cost
- Scripting/CI: cheaper fast model plus low `--max-iterations`

Use `/doctor` or `yottacode doctor` when a model is configured but not visible to the provider.
