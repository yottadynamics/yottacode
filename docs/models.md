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

## No silent fallback

If the model or base URL is missing, yottacode exits with a clear error. It does not silently default to localhost or a paid cloud provider.

## Choosing a model

Practical starting points:

- Local/privacy-first: Ollama with Qwen, Llama, or DeepSeek models
- General coding: a strong OpenAI-compatible coding model
- Deep planning: a reasoning model, with higher latency/cost
- Scripting/CI: cheaper fast model plus low `--max-iterations`

Use `/doctor` or `yottacode doctor` when a model is configured but not visible to the provider.
