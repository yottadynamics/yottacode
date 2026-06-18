# Security Policy

Thank you for helping keep yottacode and its users safe. yottacode can read,
edit, and execute code in real repositories, so security reports are handled
privately and with priority.

## Supported versions

yottacode is pre-1.0 and moving quickly. Security fixes are published on the
latest release line and on `main`.

| Version | Supported |
|---|---|
| Latest release | Yes |
| `main` | Yes |
| Older pre-1.0 tags | Best effort |

If you depend on yottacode from scripts, pin a release tag and upgrade promptly
when a security release is published.

## Reporting a vulnerability

Please do **not** open a public GitHub issue for vulnerabilities.

Use one of these private channels instead:

1. GitHub's **Report a vulnerability** button under the repository's Security
   tab.
2. Email the maintainers at `hello@yottadynamics.com`.

Include as much detail as you can safely share:

- affected yottacode version or commit
- operating system and architecture
- configuration shape, with secrets redacted
- steps to reproduce
- expected impact
- whether the issue requires a malicious model response, malicious repository,
  local attacker, or remote attacker

We aim to acknowledge reports within 72 hours. After triage, we will coordinate
fix timing and disclosure with the reporter when possible.

## Security model

yottacode is a local terminal agent. It sends prompts and tool results to the
model provider you configure, and it runs tools on your machine under your user
account. The trust boundary is explicit: you choose the workspace, provider,
permissions, and approval mode.

Key controls:

- first-launch workspace trust prompt
- approval prompts for mutating filesystem tools, shell commands, and git
  mutations
- project-local `allow` / `ask` / `deny` permission rules, where `deny` wins
- write-path validation that confines filesystem mutations to the workspace and
  configured allow roots
- symlink write rejection
- deny lists for yottacode app state, git internals, and common
  secret-bearing paths
- protected reads for common credential locations such as `.env`, `~/.ssh`,
  cloud credentials, and kubeconfig
- checkpoints and rollback for recovery from approved changes

See [`docs/security-and-allow-lists.md`](docs/security-and-allow-lists.md) for
user-facing details.

## No in-process sandbox

yottacode does **not** provide an in-process sandbox. `run_bash`, filesystem
edits, git operations, MCP tools, and other tools run on the host as the current
user.

For stronger isolation, run yottacode inside a container, devcontainer, VM, or
other environment you control. Containerizing yottacode isolates every tool, not
just shell commands.

This is a deliberate scope choice: yottacode does not ship bwrap, firejail,
landlock, or pluggable syscall-sandbox backends.

## What yottacode does not collect

yottacode has no always-on telemetry collection. Unless you explicitly enable a
future opt-in telemetry path, yottacode does not upload analytics, prompts,
source code, file paths, tool arguments, crash reports, or API keys to
YottaDynamics.

Your configured model provider receives the conversation content and tool
results needed for the active session. Do not put secrets in prompts,
`USER.md`, `YOTTACODE.md`, or agent-managed memory files.

## Secrets and local state

Store provider keys in environment variables or provider-specific auth flows.
Do not commit secrets to configuration, prompts, memory files, session exports,
or issue reports.

Important local state lives under `~/.yottacode/` and per-repo `.yottacode/`.
Treat session files, memory, checkpoints, and exported transcripts as sensitive
when they contain private project context.

## Responsible disclosure

When a fix is ready, we will publish a release and document the security impact
at the level appropriate for user safety. We may delay full technical details
briefly when doing so reduces exploitation risk for users who have not yet
upgraded.

Reporters who want credit should say how they would like to be acknowledged.
