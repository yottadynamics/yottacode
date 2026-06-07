# GitHub Integration

yottacode talks to GitHub through a typed `go-github` adapter
(`internal/github`), not by shelling out to the `gh` CLI. This page
covers the auth setup, the tool surface, the permission shape, and
the rate-limit / cache / offline behavior.

## Auth setup

Token discovery walks three tiers in this order:

1. **`$GITHUB_TOKEN`** — explicit env var. CI-friendly. Pass via
   shell env or a `.envrc`; yottacode never writes to it.
2. **`gh auth token`** — opportunistic shell-out to the `gh` CLI if
   it's on `$PATH` and authenticated. Most common local path
   because users who already use GitHub workflows usually have
   `gh` installed.
3. **`~/.yottacode/github.json`** — schema `{"token": "ghp_..."}`.
   Written by `yottacode setup github`. Personal, gitignored, file
   permissions tightened to `0600`.

Run `yottacode setup github` for the interactive flow (prompts for
a fine-grained PAT, writes the file). Run `yottacode setup github
--token ghp_xxx` for a non-interactive path. Run `yottacode setup
github --remove` to delete the file.

The resolver caches its result once per process via `sync.Once`. A
user who re-auths with `gh auth login` mid-session restarts
yottacode to pick up the new token.

## Tool surface

### Read tools (no approval)

| Tool | What it returns |
|---|---|
| `gh_pr_read` | PR metadata (title, body, state, draft, base/head, mergeable, author, labels, URL). **One API call.** |
| `gh_pr_review_context` | PR metadata + diff (capped) + check-run rollup + failing-checks summary. Three API calls, one snapshot. Used by `/git-review-pr`. |
| `gh_pr_context` | Local pre-PR context (base resolution, ahead-count, push state, PR template). No network — git-local. |
| `gh_issue_read` | Issue metadata (title, body, state, labels, assignees) + recent comments. |
| `gh_issue_list` | Open issues matching label / assignee / milestone filters. |

The two read tools the model is most likely to reach for via
`run_bash gh pr view --json …` are `gh_pr_read` (body-only) and
`gh_pr_review_context` (review). Each tool's description explicitly
calls out the typed-vs-bash trade-off so the model picks correctly.

### Write tools (approval required)

| Tool | What it does | Slash command |
|---|---|---|
| `gh_pr_create` | Opens a PR (validates title, body, base) | `/git-create-pr` |
| `gh_pr_update` | Rewrites an existing PR's title + body | `/git-update-pr` |
| `gh_pr_add_comment` | Posts a conversation comment on a PR | (model-callable) |

Every write goes through the approval modal — the modal renders the
full title + body before the call lands. `--yolo`
bypasses the modal, but `deny` rules still apply.

## Slash commands

| Command | Flow |
|---|---|
| `/git-create-pr [base]` | Validates current branch + opens a PR. Default base is the repo's default branch. |
| `/git-create-issue [title]` | Creates a GitHub issue in the current repo. Optional title arg; interactively composes if omitted. |
| `/git-update-pr [ref]` | Refreshes title/body to match the current commit list. |
| `/git-review-pr [ref]` | Structured PR review: failing-checks summary, blockers, suggestions, nits. Output is local scrollback only. |
| `/git-push` | Pushes the current branch (sets upstream on first push; surfaces the PR URL when one exists). |
| `/git-implement-issue <n>` | End-to-end: fetch issue → research → plan mode → branch → implement → tests → commit → push → draft PR. |

`/git-implement-issue` is the largest of the bunch. Spec:
[`yottacode-roadmap/git-fix-issue.md`](../yottacode-roadmap/git-fix-issue.md)
(the design doc was written under the older `/git-fix-issue` name;
the shipped command uses `/git-implement-issue`).

## Permissions

Github(...) rules are documented in
[`configuration.md`](configuration.md#rule-prefixes). The verb names
are the canonical descriptors:

```json
{
  "permissions": {
    "allow": [
      "Github(read_*)",
      "Github(list_open_issues)"
    ],
    "ask": [
      "Github(create_pr)",
      "Github(create_issue)",
      "Github(update_pr)",
      "Github(add_pr_comment)"
    ],
    "deny": [
      "Github(*-merge)"
    ]
  }
}
```

Owner/repo scoping (`Github(create_pr owner/repo)`) is deferred —
every call currently resolves against the cwd's git remote.

## In-session cache

Reads are cached for the session lifetime via `CachingClient`.
Duplicate reads (same `(owner, repo, ref)` for PR tools; same
`(owner, repo, number, max_comments)` for issues; same filter
fingerprint for issue lists) make exactly one API call. Writes
pass through. `UpdatePR` invalidates the matching `ReadPR` entry so
the next read sees fresh data. `AddPRComment` does not invalidate
`ReadPR` (comments aren't in `PRDetails`).

Errors — including `ErrPRNotFound` and `ErrGitHubUnreachable` — are
NOT cached. A retry after fixing auth or after a transient network
blip always re-hits the API.

The cache lives only in process memory; the runtime tears it down
at session end. There's no TTL, no on-disk persistence, no manual
invalidation API.

## Rate limits

The typed client captures `X-RateLimit-Remaining` from every
response. `RateLimit()` on the client returns the most recent
snapshot. The doctor surfaces it in `--- github ---`:

```text
--- github ---
token: present (source=env)
probe: reachable=yes auth=yes user=octocat
rate: 4500/5000 remaining (resets in 45m0s)
github: ok
```

Soft-warn threshold is 100 remaining. `Snapshot.IsLow()` reports
true at or below; the doctor adds a warning line.

## Offline degradation

Network errors (DNS failure, refused connection, TLS handshake,
timeout) classify as `ErrGitHubUnreachable` — distinct from
`ErrGhUnavailable` (auth missing) and `ErrPRNotFound` (logical).
Callers branch on these typed sentinels so the recovery hint is
right: auth wants `gh auth login`, unreachable wants "check your
network."

The doctor probe runs a single authenticated `/user` call against
api.github.com with a 5s ceiling. A failed probe never hangs the
doctor.

## Cloud forward-compat

The same `internal/github.Interface` will back the planned cloud
`yottacode-bot` GitHub App (SaaS Phase 2). The bot will swap in a
JWT-installation-token implementation behind the same surface; the
slash commands and tools won't change. See
[`yottacode-roadmap/github-integration.md`](../yottacode-roadmap/github-integration.md)
for the design.
