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

The wizard writes provider profiles to `~/.yottacode/config.toml`.

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

## GitHub Copilot (`copilot`)

```bash
yottacode copilot-auth login

export YOTTACODE_PROVIDER=copilot
export YOTTACODE_MODEL=claude-haiku-4.5
export YOTTACODE_BASE_URL=https://api.githubcopilot.com
```

`copilot` uses GitHub's device code flow to authenticate. Model calls bill against the user's GitHub Copilot subscription. Available models depend on the subscription tier (Free, Pro, Pro+); the model picker marks plan-gated models with "upgrade plan".

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

The native Gemini adapter uses Google's HTTP API.

## xAI

```bash
export YOTTACODE_PROVIDER=xai
export YOTTACODE_MODEL=grok-4
export YOTTACODE_BASE_URL=https://api.x.ai/v1
export YOTTACODE_API_KEY=xai-...
```

xAI hosted tools:

- `web_search` is enabled by default
- `x_search` can be enabled with `YOTTACODE_ENABLE_X_SEARCH=1`
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

`/usage` cost estimates apply to paid cloud providers only. Ollama and NVIDIA NIM (`openai-compatible` pointed at `integrate.api.nvidia.com`) are excluded by design — their per-call cost is undefined for end users. OpenRouter, Groq, and other `openai-compatible` proxies are treated as paid endpoints and priced from the local catalog when the model is known. See [cost.md](cost.md).

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
| xAI | yes | no |
| Ollama | no | no |
| OpenAI-compatible (NVIDIA NIM, etc.) | no | no |

**Pasted images** — paste a screenshot path or `file:///` URL in the input; the image is sent as a native content block the model can see. Providers marked "no" receive only the text marker (no image data), avoiding API errors from text-only models.

**`read_file` images** — `read_file("photo.png")` returns the image as a visual content block in the tool result. Only Anthropic supports image blocks in tool results today; other providers receive a text label with file metadata.
