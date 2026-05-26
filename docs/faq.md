# FAQ

## What is yottacode?

yottacode is a model-agnostic terminal AI agent for real codebases. It provides an interactive TUI, a one-shot CLI, structured tools, session persistence, memory, and cross-session recall.

## Does yottacode require OpenAI?

No. It works with OpenAI, Anthropic, Gemini, Ollama, xAI, ChatGPT OAuth through `openai-auth`, GitHub Copilot through `copilot-auth`, and OpenAI-compatible endpoints. Provider behavior depends on the endpoint and model you configure.

## Can I use my ChatGPT account instead of an OpenAI API key?

Yes. Run `yottacode openai-auth login` and configure provider `openai-auth`. Tokens are stored under `~/.yottacode/auth/`, and that directory is blocked from model reads and writes.

## Can I use my GitHub Copilot subscription?

Yes. Run `yottacode copilot-auth login` and configure provider `copilot`. Available models depend on your Copilot tier (Free, Pro, Pro+). The model picker marks plan-gated models with "upgrade plan".

## Why is there no default model?

A silent default would either fail confusingly against localhost or accidentally use a paid provider. yottacode fails fast until you explicitly configure a model and base URL.

## What is the difference between `yottacode` and `yottacode run`?

`yottacode` opens the interactive TUI. `yottacode run "<prompt>"` runs one prompt and exits, which is better for scripts and CI.

## Where is state stored?

Global state:

```text
~/.yottacode/
```

Project-local state:

```text
<repo>/.yottacode/
```

## Are sessions saved automatically?

Yes. Every completed turn is saved to `~/.yottacode/sessions/`.

## Can I search old sessions?

Yes. Use `/recall <query>` in the TUI. yottacode indexes saved sessions with local SQLite FTS5.

## Does yottacode sandbox commands?

No. There is no in-process sandbox. Use a container or devcontainer for real isolation.

## Can I auto-approve trusted commands?

Yes. Use `.yottacode/permissions.json` for shared rules and `.yottacode/permissions.local.json` for personal rules.

## Can I bypass every prompt?

Launch yottacode with `--dangerously-skip-permissions` (the permissions-bypass overlay), but only in trusted contexts. Explicit deny rules still apply. The overlay is one-way per process — restart without the flag to recover.

## What should go in USER.md?

Global preferences you want in every project: communication style, coding style, test expectations, and recurring workflow preferences.

## What should go in YOTTACODE.md?

Repo-specific context: architecture, commands, conventions, gotchas, and project status. It is usually worth committing.

## How does the agent remember things across sessions?

The agent has two tools — `memory_save` and `memory_forget` — that let it write and delete typed markdown memories during a conversation. Memories live under `~/.yottacode/memory/` (user scope) and `~/.yottacode/projects/<slug>/memory/` (per-repo scope). See [Memory](memory.md) for details.

## How do I delete a memory?

Use the `/memory` picker (browse the relevant scope, press `d` on the entry) or run `yottacode memory forget --scope <user|project> <name>` from the shell.

## Can local models access the web?

They can fetch specific URLs through yottacode's local `fetch_url` tool. Provider-hosted web search is available only for providers that support it.

## How do I switch models mid-session?

Use `/model <name>` in the TUI. Use `yottacode model use <name>` to change the configured default.

## How do I check provider setup?

Use `/provider` for resolved static configuration and `/doctor` or `yottacode doctor` for an active endpoint probe.
