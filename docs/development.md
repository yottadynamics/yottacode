# Development

Building, testing, and extending yottacode.

## Build

```bash
go build -o yottacode ./cmd/yottacode
```

Requirements: Go 1.26+. Use the exact patch version in `go.mod` when validating
security fixes or reproducing CI locally. The module is pure Go, so cross-compilation is
straightforward:

```bash
GOOS=darwin  GOARCH=arm64 go build -o yottacode-darwin-arm64  ./cmd/yottacode
GOOS=linux   GOARCH=amd64 go build -o yottacode-linux-amd64   ./cmd/yottacode
```

Supported platforms are Linux and macOS (amd64 and arm64). There is no native
Windows build — Windows contributors should work inside WSL.

## Test

```bash
go test ./...                     # unit tests, fast, no live deps
go vet ./...                      # static checks
govulncheck ./...                 # reachable dependency/toolchain CVEs
go test -tags=integration ./...   # adds live-provider tests
go test -race ./...
go test -cover ./...
```

Standing rules:

- Unit tests must not require network, GPU, or external services.
- Integration tests belong behind `//go:build integration`.
- Bug fixes should ship with a regression test.
- A change is not done until `go test ./...` is green.

## Adding A Built-In Tool

Implement [`agent.Tool`](../internal/agent/tools.go). Core cwd-bound tools
(file/search/git/run helpers and any experimental tools that should also exist in
dispatch worktree children) register through
[`RegisterCoreCwdTools`](../internal/agent/toolset.go), then the TUI and oneshot
entry points pass the needed dependencies into that helper. Session-scoped tools
(GitHub, memory, plan, subagents, web, skills) still register directly in both
entry points:

- [`internal/tui/run.go`](../internal/tui/run.go)
- [`internal/oneshot/oneshot.go`](../internal/oneshot/oneshot.go)

Conventions:

- Resolve relative paths against the session working directory.
- Require approval for mutating behavior.
- Cap output so tool results do not explode transcript size.
- Return errors as normal errors, not panics.
- Add tests for schema, happy path, edge cases, and approval behavior.

## Adding A Slash Command

Add a `slashCommand` entry in
[`internal/tui/commands.go`](../internal/tui/commands.go) and implement the
handler with the usual `(Model, tea.Cmd)` signature.

If the command takes arguments, set the `Args` field so the palette can insert
the command prefix instead of executing it immediately.

## Refreshing The Embedded Model Catalog

The cloud provider model lists for Anthropic, OpenAI, Gemini, and xAI live
in [`internal/catalog/catalog.gen.json`](../internal/catalog/catalog.gen.json),
embedded into the binary at build time. When a provider ships new
models (or deprecates old ones), regenerate the file:

```bash
ANTHROPIC_API_KEY=… OPENAI_API_KEY=… GEMINI_API_KEY=… XAI_API_KEY=… \
  go run ./cmd/yotta-models refresh
```

Optional flags:

```bash
go run ./cmd/yotta-models refresh --output internal/catalog/catalog.gen.json
```

A missing key skips that provider with a warning; existing entries
for that provider in the output file are preserved untouched, so you
can refresh one provider at a time.

The script:

- Calls each provider's list-models endpoint (Anthropic
  `/v1/models`, OpenAI `/v1/models`, Gemini `/v1beta/models`, xAI
  `/v1/models`).
- Filters OpenAI to chat-completions models via prefix regex
  ([`fetch_openai.go`](../cmd/yotta-models/fetch_openai.go)) — drop
  embeddings/tts/audio/realtime/image variants. Widening for new
  families (e.g. `gpt-6`, `o5`) is a one-line PR.
- Filters xAI to Grok chat models via prefix regex
  ([`fetch_xai.go`](../cmd/yotta-models/fetch_xai.go)) — drop image
  and video generation surfaces that are not chat turn-taking models.
- Filters Gemini to entries whose `supportedGenerationMethods`
  contains `"generateContent"` — drops embedding-only and
  countTokens-only models.
- Maps each row onto the common
  [`catalog.Model`](../internal/catalog/model.go) schema with
  tristate (`*bool`) capability flags so renderers can distinguish
  "supported" / "not supported" / "unknown."
- Sorts the result by (provider, ID) so diffs across runs are
  minimal.

Commit the refreshed `catalog.gen.json` like any other source file.
There is no runtime equivalent for curated providers — users can't refresh locally because
they don't necessarily have keys for all four providers, and the
embedded catalog keeps offline-first ergonomics. Anything else
(Ollama, openai-compatible endpoints) is fetched live at picker-open
time via [`catalog.Live`](../internal/catalog/live.go).

## Adding Or Expanding A Model Adapter

Today the adapter layer supports OpenAI-compatible chat-completions endpoints
plus automatic OpenAI Responses routing for supported reasoning models. The
agent depends only on the streaming interface, so provider-specific expansion
belongs in `internal/adapter` rather than `internal/agent`.

### Provider Profiles, Diagnostics, And Probes

Provider-specific setup and validation now have two layers:

- **Static diagnostics** in [`internal/adapter/provider.go`](../internal/adapter/provider.go)
  and [`internal/adapter/diagnostics.go`](../internal/adapter/diagnostics.go)
- **Active probes** in [`internal/adapter/diagnostics.go`](../internal/adapter/diagnostics.go)

The split is intentional:

- `ProviderProfile` is the resolved static view of a config:
  - detected provider
  - routing choice (`chat-completions` vs Responses)
  - supported native capabilities
  - enabled built-in tools
  - static issues and warnings
- `Probe(ctx, cfg)` performs a lightweight live check against the configured
  endpoint, currently via `/models`, and reports:
  - endpoint reachability
  - auth status
  - selected-model visibility
  - the same resolved profile and static diagnostics

Keep these rules when extending provider support:

- Put provider-specific validation in `internal/adapter`, not in `tui`,
  `oneshot`, or `cmd`.
- Treat `/provider`, `/doctor`, and `yottacode doctor` as **consumers** of the
  diagnostics API, not as the implementation.
- Prefer cheap, broadly-supported probes first. `/models` is useful because it
  distinguishes network, auth, and model-visibility failures without spending
  prompt tokens.
- Keep probes side-effect free. They should validate configuration, not mutate
  provider state.
- Return structured data first; format human-readable output at the edge.

When adding a new provider capability check:

1. Extend `adapter.Config` and `ProviderProfile` if the capability is part of
   the resolved static surface.
2. Add static validation in `providerDiagnostics(...)`.
3. Add live validation in `Probe(...)` only if it can be done cheaply and
   reliably across that provider's endpoint shape.
4. Cover both layers with tests:
   - `internal/adapter/*_test.go` for static and probe behavior
   - consumer tests only for rendering / command wiring

### CLI And TUI Consumers

The current diagnostics consumers are:

- TUI startup/background probing in [`internal/tui/model.go`](../internal/tui/model.go)
- static `/provider` output in [`internal/tui/commands.go`](../internal/tui/commands.go)
- active `/doctor` output in [`internal/tui/commands.go`](../internal/tui/commands.go)
- shell/CI command `yottacode doctor` in [`cmd/yottacode/main.go`](../cmd/yottacode/main.go)
- oneshot preflight fail-fast in [`internal/oneshot/oneshot.go`](../internal/oneshot/oneshot.go)

If you add a new diagnostics field, update the adapter tests first, then only
the consumers that need to display it.

## Project Layout Reminder

```text
cmd/yottacode/                cobra root command
cmd/yotta-models/             model-catalog refresh tool
internal/cli/                 option resolution
internal/adapter/             provider streaming layer
internal/edit/                edit reliability libraries for safe file patching
internal/agent/               turn loop, tools, approvals
internal/permissions/         allow / ask / deny rules
internal/github/              typed go-github adapter and PR/issue tools
internal/mcp/                 Model Context Protocol clients
internal/skills/              agent skills loader, installer, and official catalog shortcuts
internal/subagents/           typed subagent runner
internal/catalog/             embedded + live model catalog
internal/checkpoint/          per-prompt checkpoints
internal/memory/              prompt memory composer and agent-managed store
internal/recall/              FTS5 session search
internal/session/             saved conversations
internal/worktree/            git worktree sessions
internal/tui/                 interactive terminal UI
internal/oneshot/             one-shot runner
internal/version/             version string
```

## Release Versioning

The release number lives in [`internal/version/version.go`](../internal/version/version.go):

```go
const Current = "0.2.0"
```

Use semantic versioning: `MAJOR.MINOR.PATCH`.

- Bump `PATCH` for backward-compatible bug fixes.
- Bump `MINOR` for backward-compatible features.
- Bump `MAJOR` for breaking changes.

Examples:

- `0.1.0` -> `0.1.1`: bug-fix release
- `0.1.0` -> `0.2.0`: new feature release without a stable-API commitment yet
- `0.1.0` -> `1.0.0`: first stable public release
- `1.2.3` -> `2.0.0`: breaking change

Before `1.0.0`, treat `0.x` as pre-stable. That means breaking changes may
still happen before the first stable release, even if they land in a minor
bump.
