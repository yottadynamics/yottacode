# Contributing to yottacode

Thanks for your interest in improving yottacode. This guide covers getting a
development environment running, the conventions we follow, and what a good
pull request looks like.

yottacode is an open-source terminal coding agent written in Go. Issues and
pull requests are welcome at
[github.com/yottadynamics/yottacode](https://github.com/yottadynamics/yottacode).

## Ways to contribute

- **Report a bug** or **request a feature** via the
  [issue templates](https://github.com/yottadynamics/yottacode/issues/new/choose).
- **Improve the docs** — both the in-repo `docs/` guides and the published site
  at [yottacode.ai/docs](https://yottacode.ai/docs/).
- **Submit a pull request** for a bug fix or feature (see below).

If you are planning a large change, open an issue first so we can agree on the
approach before you invest the time.

## Development environment

Requirements: **Go 1.26+**. CI currently uses the Go patch version configured in
[`.github/workflows/go.yml`](.github/workflows/go.yml); use that exact patch
version when validating security fixes or reproducing CI locally. yottacode is a
single, pure-Go binary with no CGo, so the Go toolchain is all you need.

Supported platforms are **Linux** and **macOS** (amd64 and arm64). There is no
native Windows build — Windows contributors should work inside WSL.

First-time setup:

```bash
git clone https://github.com/<your-user>/yottacode.git
cd yottacode
git remote add upstream https://github.com/yottadynamics/yottacode.git

go version
go mod download
go build -o yottacode ./cmd/yottacode
./yottacode --version
```

If you want to run yottacode locally after building it, run setup once:

```bash
./yottacode setup
```

Cross-compilation is straightforward:

```bash
GOOS=darwin GOARCH=arm64 go build -o yottacode-darwin-arm64 ./cmd/yottacode
GOOS=linux  GOARCH=amd64 go build -o yottacode-linux-amd64  ./cmd/yottacode
```

## Tests and checks

Run these before opening a PR:

```bash
go build -v ./...  # compile every package
go test ./...      # unit tests — fast, no network or external services
go vet ./...       # static checks
```

CI also runs `govulncheck ./...` for reachable dependency and toolchain CVEs.
Install it locally when you are changing dependencies, provider code, security
behavior, or release-sensitive paths:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

Encouraged for changes touching concurrency, providers, or live behavior:

```bash
go test -race ./...              # race detector
go test -cover ./...             # coverage
go test -tags=integration ./...  # live-provider integration tests (needs API keys)
```

Standing rules:

- **Every new capability ships with tests.** A feature is not done until it is
  covered.
- **Every bug fix ships with a regression test** that fails before the fix and
  passes after.
- **Code, tests, and docs are one unit of work** — update the relevant `docs/`
  guide in the same change.
- Unit tests must not require network, GPU, or external services. Anything that
  does belongs behind the `//go:build integration` tag.
- Docs-only changes do not need Go tests unless they change commands, examples,
  generated docs, or behavior described by tests.
- A change is not done until the relevant local checks are green.

Testing guidance by change type:

- CLI behavior: add or update focused tests under `cmd/yottacode/`.
- Agent tools: cover schema, approval behavior, path validation, success, and
  error cases under `internal/agent/`.
- Provider or model behavior: test static diagnostics and active probes under
  `internal/adapter/` before updating CLI or TUI rendering.
- TUI changes: add focused command, rendering, or interaction tests under
  `internal/tui/` where possible.
- Documentation changes: verify links, commands, and examples you touched.

CI (`.github/workflows/go.yml`) runs build, vet, vulnerability scanning, tests,
race tests, LSP smoke tests, shellcheck, and installer smoke tests on pull
requests; it must pass before a PR can merge.

## Pull request workflow

1. Fork the repository and create a topic branch off `main`.
2. For a good first issue, comment on the issue before starting so maintainers
   know someone is working on it and can share any current context.
3. Make your change, with tests and docs updated alongside the code.
4. Run the relevant local checks until green. At minimum, run `go build -v ./...`,
   `go test ./...`, and `go vet ./...` for code changes.
5. Push your branch and open a pull request against
   `yottadynamics/yottacode:main`.
6. Write a clear description: what changed, why, and how you tested it. Link the
   issue it closes (e.g. `Closes #123`).
7. Keep PRs focused — one logical change per PR is easier to review and revert.

Commit messages should be clear and imperative ("Add X", "Fix Y"). Squash noisy
work-in-progress commits before requesting review.

Small first PRs are welcome. If you get stuck, open a draft PR or comment on the
issue with what you tried, the command that failed, and the relevant output.
Maintainers are happy to help narrow the scope or point you at the right files.

## Project layout

```text
cmd/yottacode/     cobra root command
internal/cli/      option resolution
internal/adapter/  provider streaming layer
internal/agent/    turn loop, tools, approvals
internal/session/  saved conversations
internal/memory/   memory composer and agent-managed store
internal/recall/   FTS5 session search
internal/tui/      interactive terminal UI
internal/oneshot/  one-shot runner
```

For deeper internals — adding a built-in tool, a slash command, a model
adapter, or refreshing the embedded model catalog — see
[`docs/development.md`](docs/development.md).

## Security

Please do **not** open public issues for security vulnerabilities. Report them
privately through GitHub's "Report a vulnerability" button under the
repository's **Security** tab, or follow the private reporting path in
[`SECURITY.md`](SECURITY.md). For background on yottacode's approval and
path-safety model, see
[`docs/security-and-allow-lists.md`](docs/security-and-allow-lists.md).

## Code of conduct

Be respectful, constructive, and patient. We want yottacode to be a welcoming
project for contributors of all backgrounds. See
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) for the community standards and
private reporting path.
