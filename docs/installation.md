# Installation

This page covers every install path plus post-install configuration. For the fastest path (pre-built binary + setup wizard), see the [Quick start](../README.md#quick-start) on the project README.

## Requirements

- Go 1.25+ for building from source
- A modern terminal for the interactive TUI
- A model provider: Ollama, OpenAI, Anthropic, Gemini, xAI, ChatGPT OAuth through `openai-auth`, or any OpenAI-compatible `/v1` API

## Pre-built binaries

Linux and macOS binaries (amd64 + arm64) ship on the [releases page](https://github.com/yottadynamics/yottacode/releases).

```bash
VERSION=0.1.0
# Swap linux/darwin and amd64/arm64 to match your machine
curl -fsSL https://github.com/yottadynamics/yottacode/releases/download/v${VERSION}/yottacode_${VERSION}_linux_amd64.tar.gz \
  | tar -xz
sudo install -m 0755 ./yottacode /usr/local/bin/yottacode
```

Archive matrix: `yottacode_${VERSION}_{linux,darwin}_{amd64,arm64}.tar.gz`. Each release also publishes a `SHA256SUMS` file for verification.

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
