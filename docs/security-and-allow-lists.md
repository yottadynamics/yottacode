# Security and allow lists

yottacode is designed to be explicit about risk. It can inspect, edit, test, and run commands in your project, so approval and path policy matter.

## Folder trust

On first launch in a directory yottacode prompts:

```
Accessing workspace:

  /home/me/my-repo

Quick safety check: is this a project you created or one you trust?
(Your own code, a well-known open-source project, or work from your team.)
If not, take a moment to review what's in this folder first.

yottacode will be able to read, edit, and execute files here.

  [1] Yes, I trust this folder
  [2] No, exit
```

The decision is recorded in `~/.yottacode/trusted-roots.json`. Every subfolder of a trusted root inherits the trust automatically, so cloning a new repo under an already-trusted parent does not re-prompt.

**Skip the prompt:**

- `--allow-paths <dir>` or `YOTTACODE_ALLOW_PATHS=<dir>` — passing the cwd's tree as an allow-paths root satisfies the gate session-only (no write to `trusted-roots.json`).
- `YOTTACODE_TRUST_ALL=1` — CI escape hatch. Also session-only.
- `yottacode run` (non-interactive) — trust verification is skipped entirely, matching Claude Code's `-p` behavior.
- `--dangerously-skip-permissions` does **not** skip the trust gate on its own. Combine with `YOTTACODE_TRUST_ALL=1` for fully unattended runs.

**Manage trust roots:**

```bash
yottacode trust list                  # show every trusted root + grant timestamp
yottacode trust add [path]            # default: cwd
yottacode trust remove <path>         # exact-path match
yottacode trust clear                 # remove every entry
```

**Worktrees and trust.** `yottacode --worktree <name>` requires the
repo root to be trusted; it refuses on an untrusted clone with a
one-line hint to run `yottacode` in the repo once. Worktree
directories live in user home at
`~/.yottacode/worktrees/<repo-slug>/<name>/`. The first-launch trust
gate inside a worktree session resolves back to the originating repo
via `git rev-parse --git-common-dir`, so the worktree dir itself
never needs to appear in `~/.yottacode/trusted-roots.json` — trust
on the repo flows through. An active worktree session can write
inside its own cwd normally; write validation reads cwd dynamically
so an in-session `enter_worktree` swap doesn't leave the validator
locked to the pre-swap perimeter. See [worktrees.md](worktrees.md).

**Out-of-workspace writes.** When the model tries to write a file outside cwd + `--allow-paths` roots, yottacode shows an inline elevation prompt:

```
Write outside workspace

Requested path:
  /home/me/elsewhere/notes.md

Session workspace:
  /home/me/my-repo

[1] Allow once         — trust just this file for the session
[2] Trust for session  — trust this directory and every subfolder
[3] Reject             — model sees the original error
```

`[1]` and `[2]` are session-scoped — they do **not** write to `trusted-roots.json`. Cross-session expansion still goes through `--allow-paths` or `yottacode trust add`.

## Approval model

By default:

- read-only tools run without prompting
- mutating filesystem tools prompt
- shell commands prompt
- git read-only commands run without prompting
- git mutations prompt
- destructive flags are called out in previews

Approval prompts can be answered once or turned into a reusable allow rule.

### Gate precedence

Tool calls flow through layered gates in this order:

1. **`Deny` rules** in `permissions.json` always win.
2. **Plan-mode gate** (only when plan mode is active) — blocks every mutating tool except `todo_write`, `exit_plan_mode`, and writes to the resolved plan file. Returns a structured error to the model so it can switch to a read-only or plan-file alternative.
3. **Permissions-bypass auto-allow** (only when `--dangerously-skip-permissions` was passed at startup) — every tool auto-allows silently. No safety floor.
4. **Plan-mode auto-allow** — writes to the resolved plan file are the model's only legitimate mutation surface while planning; they auto-allow without a prompt.
5. **Auto-mode auto-allow** (only when auto mode is active) — non-safety-floor mutating tools auto-allow. Safety floor (`run_bash`, `git_commit`, `git_checkpoint`, `rollback`) normally still prompts, with one carve-out: `run_bash` calls whose every segment uses a verb from a built-in read-only allowlist (`ls`, `cat`, `head`, `tail`, `wc`, `grep`, `rg`, `find`, `awk`, `cut`, `sort`, `uniq`, `diff`, `cd`, `pwd`, `which`, `echo`, `date`, `tree`, `stat`, `file`, `du`, `df`, …) AND carries no risk flag (no `>` redirects, no pipe-into-shell, no sudo) auto-allow under Source `auto-mode-safe-bash`. The intent: a model's habitual `cd <project> && grep …` chain doesn't break flow, while any mutation (rm, mv, touch, curl, go test, sed -i, …) still prompts.
6. **`Allow` rules** in `permissions.json` skip the prompt.
7. **`Ask` rules** force a prompt even on tools that would normally auto-execute.
8. **Tool-default policy** (the tool's own `RequiresApproval`) prompts mutating tools and auto-executes read-only ones.

`Deny` always wins, including over permissions bypass. `--dangerously-skip-permissions` is "skip prompts," not "ignore my policy."

Trust controls separate into **modes** (workflow shape, mutually exclusive) and the **permissions-bypass overlay** (orthogonal startup flag):

| Surface | Entry point | Effect |
|---|---|---|
| Plan mode | `/plan` · `Shift+Tab` · `--permission-mode plan` | Read-only research; gated to plan file; ends with `exit_plan_mode` |
| Auto mode | `Shift+Tab` · `--permission-mode auto` (no slash command) | Edits auto, bash/commits prompt, 4× iteration cap |
| Permissions bypass | `--dangerously-skip-permissions` at startup (no slash, no keybinding) | Drops all prompts, no iteration cap; sits on top of any mode |

Mirroring Claude Code, auto mode has no slash command and permissions bypass enters only via the startup flag — these high-autonomy states are intentionally kept off the palette and off the `Shift+Tab` cycle so they can't be triggered by accident. Permissions bypass, once enabled, is one-way per process: restart without the flag to recover. The bypass banner (`⚠ permissions bypass`) takes precedence visually while it's on; when a mode (auto or plan) is also active, the mode banner picks up a `⚠ bypass` suffix.

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
- in `/plan` mode, the *only* write target outside cwd is the resolved plan file at `~/.yottacode/plans/<slug>.md` — symlink rejection and the deny list still apply

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

Rules support `allow`, `ask`, and `deny` policy. Explicit deny rules still apply even when `--dangerously-skip-permissions` is set.

## Creating allow rules from approvals

When an approval modal appears, choose the always-allow option to save a derived rule into `permissions.local.json`. The modal shows the exact rule before it is saved.

Examples:

- `Bash(go test *)`
- `Edit(internal/**)`
- `Write(docs/**)`
- `Git(commit *)`

## Permissions bypass (the danger setting)

```bash
yottacode --dangerously-skip-permissions
```

This is dangerous. It skips approval prompts for matching operations and removes the iteration cap, but explicit deny rules remain enforced. Use it only in trusted automation or disposable environments. There is no in-TUI toggle — restart without the flag to recover. Mirrors Claude Code's `--dangerously-skip-permissions` flag.

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
