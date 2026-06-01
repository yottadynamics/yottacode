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
conversation stays on your chosen **smart** model. It is opt-in,
configurable from the TUI with **`/router`** (recommended — see below) or
by hand via the `[router]` block in `~/.yottacode/config.toml`:

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

### Failover chains

Each slot can be a **failover chain** instead of a single model — give it
the plural form with a primary followed by fallbacks:

```toml
[router]
  mode         = "auto"
  fast_model   = "anthropic:claude-haiku-4-5"                   # single is fine
  smart_models = ["anthropic:claude-opus-4-6", "openai:gpt-4o"] # primary, then fallback
  policy                   = "fallback-chain"   # reused for the chains
  health_window_seconds    = 60
  health_failure_threshold = 3
```

The first entry is the primary; on failure/timeout the call falls through
to the next, using the same `policy` + health knobs as the multi-provider
router (a flapping provider is skipped until it recovers). So a subagent
or a summarization call survives a smart-model outage instead of failing.
A slot uses the singular **or** the plural form, not both.

When a fallover happens it's surfaced loudly, the same way main-thread
fallbacks already are — a warm-yellow line tagged with where it occurred:

```
↻ fallback [Explore]: anthropic/claude-opus-4-6 → openai/gpt-4o [fallback-chain]: <reason>
↻ fallback [summarize]: anthropic/claude-haiku-4-5 → openai/gpt-4o-mini: <reason>
```

The status chip and subagent cards still show the primary. The `/router`
picker has a **Smart fallback** row: Enter sets it (a one-model
fallback), `d` clears it. Chains longer than two stay in `config.toml` —
the picker shows `(+N more)` and preserves them.

The **fast slot has no fallback row** on purpose: it's summarization-only,
and summarization is non-critical (it retries next turn). Instead of a
fast chain, **the moment the fast model fails a compaction** the
summarizer **degrades to the smart model** for the next attempt — so a
fast-provider outage can't block compaction. The counter resets on a
success, so the fast model is re-probed once it recovers. (A `fast_models`
chain set by hand in `config.toml` is still honored; it's just not
surfaced in the picker.)

### Configuring from the TUI: `/router`

You don't have to edit the file by hand. **`/router`** opens a picker
with rows — Routing, Smart model, Fast model, Smart fallback — that all
act in place (the picker stays open). Toggle the Routing row on/off, and
open the model rows to pick the smart/fast models from your configured
models (the embedded catalog plus any `providers.models`). You can enable
routing first and choose the models below — routing turns on once both
are set — or pick the models and then toggle on. Selections persist to
`config.toml` and apply live; picking a catalog model also records it in
that provider's `providers.models` so the write validates. `/router on`
and `/router off` are quick shortcuts for the toggle.

**Configuring the smart model also switches your active model.** When you
set (or change) the **Smart model** and close the picker, your main
conversation switches to that model — the smart model is your primary
capable model, so this keeps the two in sync (the same as running
`/model <smart>`). Closing without changing the smart model leaves your
active model untouched.

While routing is active (`auto`) the status bar's primary segment becomes
the routing pair itself — `<smart>:<fast>` (smart model first, fast
second, short-tagged, colon-joined), e.g.
`● claude-opus-4-6:claude-haiku-4-5 · ctx …`. The active session model is
not shown separately because configuring the smart model also switches
your active model to it (see above), so the smart half already names what
your interactive turns run on and the fast half names summarization.
(`manual` mode keeps the active model and adds a dim `routing: manual`
note; `off` shows just the active model — the picker toggles between
`off` and `auto`.)

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

Routing either to a *different* model never churns the main thread's
cache. Summarization runs on the cheaper `fast_model` — a pure saving —
and subagents run on `smart_model`. Your interactive turns are untouched.

### Modes

| `mode` | Behavior |
|---|---|
| `off` (default) | Routing disabled. Everything runs on your active model. Fully backward compatible. |
| `manual` | Resolves `fast_model` / `smart_model`, but only routes a subagent when its definition declares an explicit `model:` (see [subagents.md](subagents.md)). Non-annotated agents inherit your active model, exactly as with routing off. |
| `auto` | **Summarization / history compaction** → `fast_model`. **Every delegated subagent** (`Explore`, `Plan`, `general-purpose`, `verification`, and your custom agents) → `smart_model` — the capable model, not your active session model. An explicit `model:` on an agent definition overrides this. |

The split is deliberate: summarization is mechanical compression of
already-decided content, so it's the one place a cheaper model is a safe
saving. Subagent work — searching, planning, verifying — feeds straight
back into the main agent's reasoning, so it runs on the capable
`smart_model` by default; routing it to a weak model risks bad research
that the main agent then acts on. The fast model is therefore reserved
for summarization; a subagent reaches it only when you **explicitly** pin
it with a `model:` frontmatter (which always wins over the default). Your
**main conversation** is never affected either way. Note that
`smart_model` is independent of your active model, so you can even
delegate subagent work to a *stronger* model than the one you're chatting
on.

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
