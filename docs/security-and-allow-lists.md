# Security and allow lists

yottacode is designed to be explicit about risk. It can inspect, edit, test, and run commands in your project, so approval and path policy matter.

## Approval model

By default:

- read-only tools run without prompting
- mutating filesystem tools prompt
- shell commands prompt
- git read-only commands run without prompting
- git mutations prompt
- destructive flags are called out in previews

Approval prompts can be answered once or turned into a reusable allow rule.

## No in-process sandbox

yottacode does not sandbox tools inside its own process. `run_bash`, file edits, git commands, and other tools run on the host.

For real isolation, run yottacode itself inside a container or devcontainer. That isolates every tool, not just shell commands.

This is a deliberate scope choice: yottacode does not ship bwrap, firejail, landlock, or pluggable sandbox backends.

## Write-path validation

Mutating filesystem tools are constrained before they run:

- paths must be inside the current working directory or configured extra allow roots
- symlink writes are rejected
- yottacode app state is denied
- canonical `.git` internals are denied
- `.git/hooks/` is deliberately allowed

Extra write roots:

```bash
export YOTTACODE_ALLOW_PATHS=/home/me/shared-configs,/home/me/other-repo
```

## Read protection

Read tools do not prompt, so yottacode blocks common secret-bearing paths from silent reads, including examples like:

- `~/.ssh`
- `~/.aws`
- `~/.gnupg`
- `~/.netrc`
- `~/.kube/config`
- `~/.docker/config.json`
- `<cwd>/.env`
- `<cwd>/.env.local`

If you truly need to inspect a protected file, do it through an explicit shell command that prompts for approval.

## Permission files

Project-local permission rules live in:

```text
<repo>/.yottacode/permissions.json
<repo>/.yottacode/permissions.local.json
```

Use:

- `permissions.json` for team-shared rules that can be committed
- `permissions.local.json` for personal rules that should be gitignored

Add this to `.gitignore`:

```gitignore
.yottacode/permissions.local.json
```

## Rule shape

```json
{
  "permissions": {
    "allow": ["Bash(go test *)", "Edit(internal/**)"],
    "deny": ["Bash(rm *)"]
  }
}
```

Rules support `allow`, `ask`, and `deny` policy. Explicit deny rules still apply even when `--bypass-permissions` is set.

## Creating allow rules from approvals

When an approval modal appears, choose the always-allow option to save a derived rule into `permissions.local.json`. The modal shows the exact rule before it is saved.

Examples:

- `Bash(go test *)`
- `Edit(internal/**)`
- `Write(docs/**)`
- `Git(commit *)`

## Bypass mode

```bash
yottacode --bypass-permissions
```

This is dangerous. It skips approval prompts for matching operations, but explicit deny rules remain enforced. Use it only in trusted automation or disposable environments.

## Provider-hosted search allow lists

Provider-native web search can be restricted with domain filters:

```bash
export YOTTACODE_SEARCH_ALLOWED_DOMAINS=docs.example.com,github.com
export YOTTACODE_SEARCH_EXCLUDED_DOMAINS=spam.example
```

xAI `x_search` can be restricted with handle and date filters:

```bash
export YOTTACODE_X_SEARCH_ALLOWED_HANDLES=xai,openai
export YOTTACODE_X_SEARCH_EXCLUDED_HANDLES=badhandle
export YOTTACODE_X_SEARCH_FROM_DATE=2026-01-01
export YOTTACODE_X_SEARCH_TO_DATE=2026-12-31
```

## Secrets guidance

Do not put secrets in prompts, `USER.md`, `YOTTACODE.md`, or any agent-managed memory file. Those files are included in prompts sent to your configured model provider.

Keep API keys in environment variables, not in `config.toml`.
