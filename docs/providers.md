# Configuring providers

yottacode can use native provider adapters where useful and OpenAI-compatible endpoints everywhere else.

## Required settings

At startup, yottacode needs:

- `model`
- `base_url`
- `api_key` for remote providers that require API-key auth

The `openai-auth` and `copilot` providers are exceptions: they use OAuth flows and store tokens under `~/.yottacode/auth/`.

You can provide them through flags, environment variables, or `~/.yottacode/config.toml`.

## Environment variables

```bash
export YOTTACODE_PROVIDER=openai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.openai.com/v1
export YOTTACODE_API_KEY=sk-...
```

Flags override environment variables:

```bash
yottacode --model <your-model-id> --base-url https://api.openai.com/v1 --api-key sk-...
```

## Setup wizard

```bash
yottacode setup
```

The wizard writes provider profiles to `~/.yottacode/config.toml`. When Ollama is running, setup probes the local server, shows detected model tags in a picker, and can enable semantic memory search with a detected or newly pulled local embedding model.

## Provider profiles

Provider profiles live in `~/.yottacode/config.toml`:

```toml
[active]
provider      = "openai"
default_model = "<your-model-id>"

[[providers]]
name          = "openai"
kind          = "openai"
base_url      = "https://api.openai.com/v1"
api_key_env   = "OPENAI_API_KEY"
default_model = "<your-model-id>"
```

Do not put raw API keys in `config.toml`. Use `api_key_env` and set the secret in your shell environment or in `~/.yottacode/.env` through the setup wizard.

## OpenAI

```bash
export YOTTACODE_PROVIDER=openai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.openai.com/v1
export YOTTACODE_API_KEY=sk-...
```

OpenAI reasoning models such as `o1*`, `o3*`, `o4*`, and `gpt-5*` are routed to the Responses API automatically when appropriate.

OpenAI hosted tools:

- `web_search` is enabled by default
- `code_interpreter` can be enabled with `YOTTACODE_ENABLE_CODE_INTERPRETER=1`

Disable default hosted web search:

```bash
export YOTTACODE_DISABLE_WEB_SEARCH=1
```

## ChatGPT OAuth (`openai-auth`)

```bash
yottacode openai-auth login

export YOTTACODE_PROVIDER=openai-auth
export YOTTACODE_MODEL=<your-model-id>     # /model list shows what your account allows
export YOTTACODE_BASE_URL=https://chatgpt.com/backend-api/codex
```

`openai-auth` signs in with a ChatGPT account through browser OAuth. Model calls use the ChatGPT-authenticated backend instead of OpenAI API keys. Available models are account-dependent; after login, yottacode probes candidate models and stores the accepted list next to the token store.

Lifecycle commands:

```bash
yottacode openai-auth login
yottacode openai-auth status
yottacode openai-auth status --json
yottacode openai-auth logout
```

Tokens and scanned model lists live in `~/.yottacode/auth/` with restrictive file permissions. That directory is denied to model read and write tools.

**Context window.** The Codex backend enforces a much smaller input limit than the same model ids have on `api.openai.com`: gpt-5.5 accepts roughly 272k input tokens here (measured 2026-06-10: 264,995 accepted, ~281k rejected) versus the 1.05M the catalog lists for the API-key provider. Window-derived behavior — the usage bar, the compaction watermark, auto-summarize — therefore resolves windows per provider *kind*: a provider-qualified entry (`openai-auth/gpt-5`, shipped at the verified-safe 264000) outranks the catalog's per-model number, so facts from one backend never leak to a namesake model behind another. To pin a different value for an exact model, add an entry like `{"prefix": "openai-auth/gpt-5.5", "window": 250000}` to `~/.yottacode/context-windows.json` — the longest matching prefix wins.

The backend also rejects `max_output_tokens` (`Unsupported parameter`), and mid-stream failures arrive as an SSE `error` event with the detail nested under `error.message` (followed by a `response.failed` event); the adapter surfaces that message verbatim, so an input-too-large turn reports `context_length_exceeded` instead of a generic stream error.

**Passive drift correction.** Advertised limits and enforced limits drift apart (the 272K-vs-1.05M case above), so yottacode also corrects windows from live traffic, for every provider kind: a turn rejected for context overflow that the local estimator thought would fit shrinks the window pin (estimate × 0.9, geometrically on repeat), and a completed turn whose exact provider-reported input exceeds the resolved window raises it to the proven value. Corrections are written as `<kind>/<model>` entries in `~/.yottacode/context-windows.json` with a `[window]` notice in the transcript, and a shrink immediately re-runs the context check — so an over-window session auto-summarizes in the same turn instead of failing again. config.toml is never touched.

## GitHub Copilot (`copilot`)

```bash
yottacode copilot-auth login

export YOTTACODE_PROVIDER=copilot
export YOTTACODE_MODEL=claude-haiku-4.5
export YOTTACODE_BASE_URL=https://api.githubcopilot.com
```

`copilot` uses GitHub's device code flow to authenticate. Model calls bill against the user's GitHub Copilot subscription. Available models depend on the subscription tier (Free, Pro, Pro+); the model picker marks plan-gated models with "upgrade plan".

**Context window.** Copilot fronts other vendors' models at its own (smaller) token limits — gpt-5.5 via Copilot is 400K, not the 1.05M the same id has on `api.openai.com`. The login scan captures each model's real limit from GitHub's models API, and window resolution reads it per provider kind, so a namesake model never inherits another backend's number. The same overlay pinning described for `openai-auth` above (`copilot/<model-id>` entries in `~/.yottacode/context-windows.json`) works here too.

Lifecycle commands:

```bash
yottacode copilot-auth login          # device code flow, saves token + caches models
yottacode copilot-auth models         # list available models (updates cache)
yottacode copilot-auth models --raw   # full API response for debugging
yottacode copilot-auth status
yottacode copilot-auth status --json
yottacode copilot-auth logout
```

In the TUI, `/provider add` with the `copilot-auth` entry runs the device code flow inline — no separate CLI step needed.

Tokens and cached model lists live in `~/.yottacode/auth/` with restrictive file permissions. That directory is denied to model read and write tools.

## Ollama

```bash
ollama serve
ollama pull qwen3.5:latest

export YOTTACODE_PROVIDER=ollama
export YOTTACODE_MODEL=qwen3.5:latest
export YOTTACODE_BASE_URL=http://localhost:11434/v1
```

Ollama does not require an API key. Provider-native hosted tools are not available; the model can use yottacode local tools such as `fetch_url` for concrete URLs.

## Anthropic

```bash
export YOTTACODE_PROVIDER=anthropic
export YOTTACODE_MODEL=claude-sonnet-4-6
export YOTTACODE_BASE_URL=https://api.anthropic.com
export YOTTACODE_API_KEY=sk-ant-...
```

The native Anthropic adapter uses the Messages API rather than an OpenAI-compatible shim.

## Gemini

```bash
export YOTTACODE_PROVIDER=gemini
export YOTTACODE_MODEL=gemini-2.5-pro
export YOTTACODE_BASE_URL=https://generativelanguage.googleapis.com
export YOTTACODE_API_KEY=...
```

The native Gemini adapter uses Google's HTTP API. Gemini is a curated provider: the default-model picker in the first-run wizard and `/provider add`, the `/model` picker, and `/model list` all read yottacode's embedded Gemini catalog plus the local models.dev snapshot, so newly listed Gemini models can appear before the generated provider catalog is refreshed. Gemini API errors are summarized to the HTTP status, Google status, primary message, and retry hint instead of dumping the full JSON error envelope.

Thinking Gemini models (Gemini 3 era) attach an opaque `thoughtSignature` to each function call and require it back when the conversation history is replayed; the adapter round-trips it automatically. For history that carries no signature — turns recorded by older yottacode versions, or a session switched to Gemini from another provider mid-conversation — the adapter substitutes Google's documented bypass token so the session keeps working.

## Google Vertex AI

Vertex serves Gemini and Claude from **your own GCP project**, so tokens bill to your Google Cloud account and traffic stays inside your project's IAM and VPC-SC boundary. There is no API key: yottacode authenticates with [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) and mints a fresh access token for every request.

```bash
gcloud auth application-default login
```

Vertex serves the two model families over different surfaces, so yottacode keeps two provider kinds internally while setup and `/provider add` show one **Google Vertex AI** row with a Gemini/Claude family dropdown plus a GCP project field. The full Vertex `base_url` is derived from that project and family so you do not have to edit the long endpoint by hand:

| Kind | Models | Surface |
|---|---|---|
| `vertex` | Gemini | the project's OpenAI-compatible chat shim |
| `vertex-anthropic` | Claude | `:streamRawPredict` (native Messages API) |

Both carry the GCP project and location inside `base_url` rather than in separate fields:

```toml
[[providers]]
name          = "vertex-claude"
kind          = "vertex-anthropic"
base_url      = "https://aiplatform.googleapis.com/v1/projects/YOUR-PROJECT/locations/global"
default_model = "claude-sonnet-4-5@20250929"

[[providers]]
name          = "vertex-gemini"
kind          = "vertex"
base_url      = "https://us-central1-aiplatform.googleapis.com/v1/projects/YOUR-PROJECT/locations/us-central1/endpoints/openapi"
default_model = "google/gemini-2.5-pro"
```

No `api_key_env` — ADC supplies the credential. `GOOGLE_APPLICATION_CREDENTIALS` may point at a service-account key instead of a user login.

Three things about model names and locations catch people out:

- **Claude ids need Vertex's version suffix** — `claude-sonnet-4-5@20250929`, not `claude-sonnet-4-5`. A bare id is not servable. Ids ending `@default` track the latest snapshot.
- **Gemini ids are publisher-namespaced** on the shim — `google/gemini-2.5-pro`.
- **Location matters** — Claude is safest on `locations/global`; Gemini's OpenAI-compatible shim is regional in practice. The Gemini URL must keep the hostname and path location in sync, for example `https://us-central1-aiplatform.googleapis.com/v1/projects/P/locations/us-central1/endpoints/openapi`.

Both kinds are curated: the `/model` picker reads yottacode's local models.dev snapshot, filtered to the family each kind can actually drive. Vertex has no list-models endpoint worth reading — the chat shim doesn't implement one, and the publisher-model endpoint returns the whole Model Garden (image classifiers, deploy-it-yourself entries, and models your region won't serve). Add anything the picker lacks under `[[providers.models]]`.

Vertex access is project-specific, so a model can appear in the public catalog but still 404 for your project/location until access is granted in Vertex Model Garden. Run an access scan after configuring a Vertex provider:

```bash
yottacode provider scan vertex-claude
# or, for the Gemini family
yottacode provider scan vertex-gemini
```

The scan sends tiny test requests with ADC, writes the result under `~/.yottacode/auth/vertex-models/<project>/<location>/`, and the `/model` picker greys out scanned models your project cannot call with `no access`. Re-run the scan after enabling new Vertex models or changing the provider's location.

Both families reason, both report thinking tokens to `/usage`, and [`/effort`](tui-slash-commands.md) steers both. Claude uses the same extended-thinking budget as the direct Anthropic provider. Gemini goes through the shim's `reasoning_effort` enum — note this is the shim's own knob, not Gemini's native `thinkingBudget`, which the shim does not expose.

## xAI

```bash
export YOTTACODE_PROVIDER=xai
export YOTTACODE_MODEL=grok-4
export YOTTACODE_BASE_URL=https://api.x.ai/v1
export YOTTACODE_API_KEY=xai-...
```

`xai` uses the embedded model catalog; maintainers refresh that catalog with `XAI_API_KEY` in `cmd/yotta-models`.

xAI hosted tools:

- `web_search` is enabled by default
- `x_search` is enabled by default for X posts, users, and threads
- `code_interpreter` can be enabled when supported

Optional filters:

```bash
export YOTTACODE_SEARCH_ALLOWED_DOMAINS=docs.x.ai,arxiv.org
export YOTTACODE_X_SEARCH_ALLOWED_HANDLES=xai
export YOTTACODE_X_SEARCH_FROM_DATE=2026-01-01
export YOTTACODE_X_SEARCH_TO_DATE=2026-12-31
```

## Custom OpenAI-compatible endpoints

Use provider kind `openai-compatible` or pass the base URL directly:

```bash
export YOTTACODE_PROVIDER=openai-compatible
export YOTTACODE_MODEL=llama-3.1-70b
export YOTTACODE_BASE_URL=https://example.com/v1
export YOTTACODE_API_KEY=...
```

This works with many gateways and self-hosted runtimes that expose `/v1/chat/completions` and `/v1/models`.

Tested examples include NVIDIA NIM, Groq, vLLM, and Llama Stack. Other gateways that speak the same wire protocol should work but are not formally validated.

**Tool-argument tolerance.** Some open models on these endpoints emit numeric
and boolean tool arguments as JSON *strings* — `{"max_results":"5"}` instead of
`{"max_results":5}`. This is a model trait, not a host one: Meta Llama 3.1/3.3
instruct do it (on NIM, Ollama, vLLM, etc.), while NVIDIA's own Nemotron, Mistral,
Qwen, and DeepSeek emit properly-typed JSON. yottacode normalizes these against
each tool's schema before the tool runs, so affected models work without
configuration. A model that instead emits the whole tool call as plain text
(rather than a structured call) is a separate limitation that normalization
cannot fix.

`/usage` reports token usage for every provider and links the billing dashboard for the paid cloud ones; it does not compute a dollar figure (no provider exposes per-model pricing on the inference key). Ollama and NVIDIA NIM (`openai-compatible` pointed at `integrate.api.nvidia.com`) have no billing dashboard — token counts only — and yottacode omits optional streaming usage probes for them to stay compatible with stricter OpenAI-compatible gateways. See [cost.md](cost.md).

## Diagnostics

Inside the TUI:

```text
/provider
/doctor
```

From the shell:

```bash
yottacode doctor
yottacode doctor --json
```

`/provider` shows static resolved config. `/doctor` performs an active `/models` probe for endpoint reachability, auth, and model visibility.

## Switch providers

```bash
yottacode provider list
yottacode provider use openai
```

In the TUI, use the provider picker or:

```text
/provider use openai
```

Switching provider in an active session rebuilds the adapter while preserving the session history.

## Image support

Image support varies by provider. Two capabilities matter:

| Provider | Pasted images | `read_file` images |
|---|---|---|
| Anthropic | yes | yes |
| OpenAI | yes | no |
| ChatGPT OAuth (`openai-auth`) | yes | no |
| GitHub Copilot | yes | no |
| Gemini | yes | no |
| Vertex AI (`vertex`, `vertex-anthropic`) | yes | no |
| xAI | yes | no |
| Ollama | no | no |
| OpenAI-compatible (NVIDIA NIM, etc.) | no | no |

**Pasted images** — paste a screenshot path or `file:///` URL in the input; the image is sent as a native content block the model can see. Providers marked "no" receive only the text marker (no image data), avoiding API errors from text-only models.

**`read_file` images** — `read_file("photo.png")` returns the image as a visual content block in the tool result. Only Anthropic supports image blocks in tool results today; other providers receive a text label with file metadata.

## Reasoning effort

Set how hard a reasoning-capable model thinks with [`--reasoning-effort`](configuration.md) (or `YOTTACODE_REASONING_EFFORT`) at launch, or [`/effort`](tui-slash-commands.md) mid-session. The surface is uniform — `default · low · medium · high` — but each provider has a different underlying knob, so yottacode translates the level per provider:

| Provider | Underlying knob | Notes |
|---|---|---|
| OpenAI (`gpt-5*`, `o1`/`o3`/`o4`) | `reasoning.effort` enum | `low`/`medium`/`high` map 1:1. Non-reasoning models (e.g. `gpt-4o`) ignore it. |
| ChatGPT OAuth (`openai-auth`) | `reasoning.effort` enum | Same as OpenAI, on the Codex backend. |
| Anthropic (Claude) | extended-thinking token budget | Enables thinking with a budget sized as a fraction of the model's max-output tokens (low ≈ 25%, high ≈ 75%); `max_tokens` is raised to leave room for the answer. A model the catalog doesn't know falls back to a conservative budget so effort still engages — refresh the catalog (`yotta-models refresh`) for the full model-scaled budget. |
| Gemini (2.5) | `thinkingConfig.thinkingBudget` | Enables thinking with a budget scaled per level, capped to the Gemini 2.5 family's valid range. |
| Vertex AI — Claude (`vertex-anthropic`) | extended-thinking token budget | Same as Anthropic. The `@version` suffix is stripped for the catalog lookup, so a pinned snapshot gets the same model-scaled budget as the bare id. |
| Vertex AI — Gemini (`vertex`) | `reasoning_effort` enum | The chat shim's own knob (`low`/`medium`/`high` map 1:1), not Gemini's native `thinkingBudget`, which the shim doesn't expose. Measured: `gemini-2.5-pro` spends ~800 thinking tokens at `low` against ~7,600 at `high`. |
| xAI (Grok) | `reasoning_effort` enum | Only `grok-*-mini` accepts it (`low`/`high`; `medium` folds to `high`). `grok-4` reasons unconditionally and is left untouched. |

**Default is unchanged.** When effort is unset, yottacode injects no reasoning parameter at all — every provider behaves exactly as it does without the setting. In particular, Anthropic and Gemini do **not** get extended thinking unless you ask for it (it costs extra tokens). `/effort default` (or `off`/`none`) returns to this state mid-session.

The TUI status bar shows `effort: default|low|medium|high` only when the active provider/model accepts the effort option; unsupported models omit the chip instead of advertising a no-op setting.

**No per-model table to maintain.** Whether a model supports thinking, and how big a thinking budget to allow, come from the model catalog (`Capabilities.Thinking` and `MaxOutput`, fetched from each provider's list-models endpoint via `yotta-models refresh`) — not a hand-maintained list. A model the catalog doesn't describe still works: enum providers (OpenAI/xAI) are gated on model-name prefixes, and Anthropic/Gemini fall back to a conservative thinking budget. If the catalog explicitly marks a model as non-thinking, the effort is a no-op rather than an error (surfaced as a `/effort` hint).
