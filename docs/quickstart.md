# Quickstart

This guide gets yottacode running against a model provider and starts your first coding session.

## 1. Install yottacode

One-liner (Linux + macOS, amd64 + arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh | bash
```

The installer drops `yottacode` into `~/.yottacode/bin/` (no `sudo`), verifies the release archive against published SHA256 sums, and appends a `PATH` export to your shell rc — making a timestamped backup of the rc first.

Skip the rc edit (you manage `PATH` yourself):

```bash
curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh \
  | bash -s -- --no-modify-rc
```

Pin a specific version instead of "latest":

```bash
curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh \
  | VERSION=0.2.0 bash
```

Verify the install:

```bash
yottacode --version
```

After install, yottacode checks GitHub for a newer release once a day on TUI startup and shows an in-session notice when one is available. The check runs in the background so GitHub availability does not block the TUI from opening. Set `YOTTACODE_NO_UPDATE_CHECK=1` to disable it entirely. Windows users should run yottacode under WSL.

For build-from-source, cross-compile, and pinned-archive paths, see [Installation](installation.md).

## 2. Configure a provider

yottacode does not guess a default model or endpoint. The fastest way to configure one is the wizard.

### Run the wizard (recommended)

```bash
yottacode setup
```

<!-- Screenshot: drop ![Setup wizard](assets/setup-wizard.png) here once captured -->

The wizard writes `~/.yottacode/config.toml` and `~/.yottacode/.env`, probes providers where possible, shows detected Ollama models in a picker, and can be rerun later with `/setup` from inside the TUI. The final review screen shows the config path, `.env` write, active provider/model, and a focused action picker so you can write, go back, or abort before anything is saved.

### Or configure manually

You need at minimum:

- a model id
- a provider base URL
- an API key for providers that require one

Pick one of the paths below; see [Providers](providers.md) for the full list.

#### Local Ollama

```bash
ollama serve
ollama pull <your-model-id>

export YOTTACODE_PROVIDER=ollama
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=http://localhost:11434/v1

yottacode
```

Ollama ignores API keys; yottacode handles that internally.

#### OpenAI

```bash
export YOTTACODE_PROVIDER=openai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.openai.com/v1
export YOTTACODE_API_KEY=sk-...

yottacode
```

#### ChatGPT OAuth (`openai-auth`)

```bash
yottacode openai-auth login

export YOTTACODE_PROVIDER=openai-auth
export YOTTACODE_MODEL=<your-model-id>     # /model list shows what your account allows
export YOTTACODE_BASE_URL=https://chatgpt.com/backend-api/codex

yottacode
```

`openai-auth` uses a browser "Sign in with ChatGPT" flow instead of an API key. Saved tokens live under `~/.yottacode/auth/`, which yottacode blocks from model reads and writes.

#### GitHub Copilot (`copilot`)

```bash
yottacode copilot-auth login

export YOTTACODE_PROVIDER=copilot
export YOTTACODE_MODEL=claude-haiku-4.5
export YOTTACODE_BASE_URL=https://api.githubcopilot.com

yottacode
```

`copilot` uses GitHub's device code flow. Model calls bill against the user's GitHub Copilot subscription. Available models depend on the subscription tier (Free, Pro, Pro+); the model picker marks plan-gated models.

#### xAI

```bash
export YOTTACODE_PROVIDER=xai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.x.ai/v1
export YOTTACODE_API_KEY=xai-...

yottacode
```

## 3. Ask for help in the TUI

Launch yottacode from a project directory:

```bash
cd ~/src/my-project
yottacode
```

Try prompts like:

```text
summarize this repository
```

```text
find the tests for the session package and explain how persistence works
```

```text
add a regression test for the bug described in this issue
```

Useful interactive commands:

- `/help` — list slash commands
- `/provider` — add, remove, or switch providers
- `/model` — open model picker or `/model <name>` to switch directly
- `/plan` — toggle plan mode (also Shift+Tab) for research before implementation
- `/theme` — change the color theme
- `/skills` — menu to enable/disable, install, uninstall, check, and update skills
- `/sessions` — resume, rename, or export sessions
- `/memory` — edit USER.md / YOTTACODE.md or browse agent-managed memories
- `/doctor` — actively probe the configured endpoint
- `/init` — draft or refresh `.yottacode/YOTTACODE.md`

## 4. Use one-shot mode for scripts

```bash
yottacode run "summarize the public API of this repo"
```

`yottacode run` prints the final answer to stdout and sends status/tool progress to stderr, so it composes cleanly with pipes, redirects, and CI logs.

## 5. Understand approvals

yottacode reads and inspects your repo without prompting; mutating actions usually ask first. You will see approval modals for file writes, shell commands, destructive git operations, and similar changes.

For real isolation, run yottacode inside a container or devcontainer. yottacode does not provide an in-process sandbox.

## Next steps

- [CLI usage](usage/cli.md)
- [TUI slash commands](usage/tui-slash-commands.md)
- [Configuring providers](providers.md)
- [Security and allow lists](security-and-allow-lists.md)
- [Memory](memory.md)
