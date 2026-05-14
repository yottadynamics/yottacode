# Installation

This page covers every install path plus post-install configuration. For the fastest path (pre-built binary + setup wizard), see the [Quick start](../README.md#quick-start) on the project README.

## Requirements

- Go 1.25+ for building from source
- A modern terminal for the interactive TUI
- A model provider: Ollama, OpenAI, Anthropic, Gemini, xAI, ChatGPT OAuth through `openai-auth`, or any OpenAI-compatible `/v1` API

## Installer script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh | bash
```

The script detects your OS/arch (Linux + macOS, amd64 + arm64), resolves the latest GitHub release (or honors `VERSION=0.3.0` if set), verifies the archive against `SHA256SUMS`, and installs to `$HOME/.yottacode/bin/yottacode`. No `sudo` required. It then offers to append a `PATH` export to your shell rc file (zsh / bash / fish / sh detected from `$SHELL`), backing up the rc first.

Useful flags and env:

- `VERSION=0.3.0` — pin a version instead of "latest".
- `INSTALL_DIR=/custom/path` — override the install location.
- `--no-modify-rc` — skip the rc-file edit (useful when you manage `PATH` yourself).
- `--yes` / `-y` — non-interactive: assume "yes" to prompts (required in CI).
- `NO_COLOR=1` — disable ANSI colors and animations.

Re-running the installer upgrades in place: same flow, the rc edit is detected and skipped via sentinel comments.

## Updating

`yottacode` checks GitHub for a newer release once per day on startup. The check is asynchronous, cached at `~/.yottacode/cache/update-check.json`, and runs **only** when the root interactive command launches into a real terminal — `yottacode run`, `yottacode --version`, scripts, and pipes never trigger it. When a newer release exists, you'll see a one-line prompt before the TUI starts:

```
yottacode 0.3.0 is available (you have 0.2.0).
Release notes: https://github.com/yottadynamics/yottacode/releases/tag/v0.3.0
Install now? [y/N]:
```

Answer `y` and the installer runs in the foreground; yottacode exits cleanly once it's done so you can re-launch on the new binary. Answer anything else and the TUI starts as normal.

To disable the check entirely (CI, privacy, sandboxes): `export YOTTACODE_NO_UPDATE_CHECK=1`. To force a refresh: `rm ~/.yottacode/cache/update-check.json`.

## Manual binary install

<details><summary>Pinned download + manual extract</summary>

```bash
VERSION=0.2.0
# Swap linux/darwin and amd64/arm64 to match your machine
curl -fsSL https://github.com/yottadynamics/yottacode/releases/download/v${VERSION}/yottacode_${VERSION}_linux_amd64.tar.gz \
  | tar -xz
install -m 0755 ./yottacode "$HOME/.yottacode/bin/yottacode"
```

Archive matrix: `yottacode_${VERSION}_{linux,darwin}_{amd64,arm64}.tar.gz`. Each release also publishes a `SHA256SUMS` file for verification.

</details>

## Build from source

```bash
git clone https://github.com/yottadynamics/yottacode.git
cd yottacode
go build -o yottacode ./cmd/yottacode
```

Run it from the repo:

```bash
./yottacode --help
```

Install it onto your `PATH`:

```bash
sudo install -m 0755 ./yottacode /usr/local/bin/yottacode
```

Or install into a user-local bin directory:

```bash
mkdir -p ~/.local/bin
install -m 0755 ./yottacode ~/.local/bin/yottacode
```

Make sure `~/.local/bin` is on your `PATH`.

## Go install from a local checkout

From the repository root:

```bash
go install ./cmd/yottacode
```

This writes the binary to `$(go env GOPATH)/bin`.

## Cross-compile

```bash
GOOS=darwin GOARCH=arm64 go build -o yottacode-darwin-arm64 ./cmd/yottacode
GOOS=darwin GOARCH=amd64 go build -o yottacode-darwin-amd64 ./cmd/yottacode
GOOS=linux  GOARCH=arm64 go build -o yottacode-linux-arm64  ./cmd/yottacode
GOOS=linux  GOARCH=amd64 go build -o yottacode-linux-amd64  ./cmd/yottacode
```

## Supported platforms

- Linux: supported by source builds and release binaries
- macOS: supported by source builds and release binaries
- Windows: not a release target; run yottacode under WSL

## Run the setup wizard (recommended)

yottacode does not guess a default model or endpoint. The fastest post-install path is the interactive wizard:

```bash
yottacode setup
```

The wizard writes `~/.yottacode/config.toml` and `~/.yottacode/.env`, probes providers where possible, and can be rerun later with `/setup` from inside the TUI.

## Or configure manually

### Local Ollama (no API key)

```bash
ollama serve
ollama pull <your-model-id>

export YOTTACODE_PROVIDER=ollama
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=http://localhost:11434/v1

yottacode
```

### ChatGPT OAuth (`openai-auth`)

```bash
yottacode openai-auth login

export YOTTACODE_PROVIDER=openai-auth
export YOTTACODE_MODEL=<your-model-id>     # /model list shows what your account allows
export YOTTACODE_BASE_URL=https://chatgpt.com/backend-api/codex

yottacode
```

`openai-auth` stores tokens under `~/.yottacode/auth/` with restrictive permissions; that directory is blocked from model reads and writes.

Other OpenAI-compatible endpoints (NVIDIA NIM's free tier, Groq, vLLM, ...) work the same way — see [Configuring providers](providers.md).
