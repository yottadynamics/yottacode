# Security and allow lists

Bash permission hardening is documented in the **Bash rule matching** section below.

Yottacode is designed to be explicit about risk. It can inspect, edit, test, and run commands in your project, so approval and path policy matter.

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
- `--yolo` does **not** skip the trust gate on its own. Combine with `YOTTACODE_TRUST_ALL=1` for fully unattended runs.

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

## Sensitive projects

Folder trust governs what yottacode may *write*. Sensitive projects govern what leaves the machine *on their own* — specifically, automatic session recall.

Everything about recall is local: embedding, storage, and cosine similarity all run against `~/.yottacode/index.sqlite`. The exposure is narrower than that and easy to miss: an excerpt that gets **injected** travels to the cloud LLM with the turn. For a PHI/medical or otherwise regulated repository, that is not acceptable, so those repos can be quarantined:

```bash
yottacode sensitive list              # show every sensitive root + marking timestamp
yottacode sensitive add [path]        # default: cwd
yottacode sensitive remove <path>     # exact-path match
yottacode sensitive clear             # remove every entry
```

The marking is recorded in `~/.yottacode/sensitive-roots.json` and covers every subfolder, so marking the repository root is enough regardless of which subdirectory a session started in. It takes effect on the next launch — the posture is resolved once at startup — and the session announces it, so you can confirm the protection is live rather than inferring it from an absence.

**It cuts both ways**, which is the part worth being explicit about:

- **Inbound** — automatic recall is off inside a sensitive project. Nothing is swept into its prompts.
- **Outbound** — a sensitive project's conversations are never candidates for *any other* project's recall, whatever `retrieval.session_recall.scope` is set to. Without this half, setting `scope = "user"` would quietly carry PHI into an unrelated repo's turn — the same leak facing the other direction.

**What it deliberately does not do.** Sessions are still saved, indexed, and embedded, and the manual `session_recall` tool still reaches them. The gate is about what leaves *automatically*, not about making your own history unreachable when you deliberately ask for it. It also does not touch semantic memory retrieval — memories are things the agent chose to write down, a different trust model from an automatic sweep over raw transcripts.

Like `trusted-roots.json`, the store is on the agent's deny-list, so the model cannot un-mark a project and re-enable egress for it.

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

Approval prompts can be answered once or turned into a reusable allow rule. Auto-approved calls do not render boxed notice cards in the transcript; they render as one-line system messages such as `✓ auto · edit_file(...) · auto-mode` or `✓ allowed by rule · write_file(...) · permissions` so approval state stays visible without adding multi-line noise.

### Gate precedence

Tool calls flow through layered gates in this order:

1. **`Deny` rules** in the system, project-shared, and project-local policy files always win.
2. **Plan-mode block** (only when plan mode is active) — blocks every mutating tool except `todo_write`, `exit_plan_mode`, and writes to the resolved plan file. Returns a structured error to the model so it can switch to a read-only or plan-file alternative. Beats an `Ask` rule too: plan mode's read-only invariant isn't something a confirm-and-proceed prompt should be able to soften.
3. **Unattended background-worker policy** (only for background subagents) — allows worktree-confined file edits but denies host shell, tests, git commits, and network-facing mutations unless that worker is running inside the command sandbox. Background workers have no human to answer a prompt, so they're routed to this deterministic policy before `Ask` or any mode overlay ever applies.
4. **`Ask` rules** force a prompt even on tools that would normally auto-execute, and even when yolo, auto mode, or plan-mode's plan-file auto-allow is on. An explicit `Ask` rule is a standing "always confirm this" policy, not a default the active mode gets to override — the same footing as `Deny`, just for a prompt instead of a refusal.
5. **Plan boundary tools** (`enter_plan_mode`, `exit_plan_mode`) always prompt. The approval card is the mode-change handshake, so auto/yolo/allow rules do not bypass it.
6. **Yolo mode auto-allow** (when `--yolo` was passed at startup, `/yolo` toggled the overlay on, or the Shift+Tab cycle reaches yolo) — every remaining non-boundary tool auto-allows. No safety floor.
7. **Plan-mode auto-allow** — writes to the resolved plan file are the model's only legitimate mutation surface while planning; they auto-allow without a prompt.
8. **Auto-mode auto-allow** (only when auto mode is active) — non-safety-floor mutating tools auto-allow. Safety floor (`run_bash`, `run_tests`, `git_commit`, `git_checkpoint`, `rollback`, `enter_worktree`, `exit_worktree`) normally still prompts, with one carve-out: `run_bash` calls whose every segment uses a verb from a built-in read-only allowlist (`ls`, `cat`, `head`, `tail`, `wc`, `grep`, `rg`, `find`, `awk`, `cut`, `sort`, `uniq`, `diff`, `cd`, `pwd`, `which`, `echo`, `date`, `tree`, `stat`, `file`, `du`, `df`, …) AND carries no risk flag (no `>` redirects, no pipe-into-shell, no sudo, no credential-store path) auto-allow under Source `auto-mode-safe-bash`. The intent: a model's habitual `cd <project> && grep …` chain doesn't break flow, while any mutation (rm, mv, touch, curl, go test, sed -i, …) still prompts. `run_bash` also has an execution-time floor that refuses `sudo`, `doas`, and `pkexec` before the shell starts, even if an approval or allow rule would otherwise let the command run.
9. **`Allow` rules** in permission files skip the prompt.
10. **Tool-default policy** (the tool's own `RequiresApproval`) prompts mutating tools and auto-executes read-only ones.

`Deny` and `Ask` both survive every mode overlay, yolo included — the same "written on purpose" reasoning applies to both, not just to `Deny`. `--yolo` / `/yolo` is "skip prompts," not "ignore my policy."

Trust controls separate into **modes** (workflow shape, mutually exclusive) and the **yolo mode overlay** (orthogonal, applies on top of any mode):

| Surface | Entry point | Effect |
|---|---|---|
| Plan mode | `/plan` · `Shift+Tab` · `--permission-mode plan` | Read-only research; gated to plan file; ends with `exit_plan_mode` |
| Auto mode | `/auto` · `Shift+Tab` · `--permission-mode auto` | Edits auto, bash/tests/commits/worktree shifts prompt, 4× iteration cap |
| Yolo mode | `--yolo` at startup · `/yolo` · `Shift+Tab` | Drops all non-boundary prompts, finite high iteration cap; sits on top of any mode |

Auto mode has a slash command (`/auto`) and is also part of the `Shift+Tab` cycle for fast in-session changes. Yolo mode is the explicit always-approve stop in the same cycle and has its own slash toggle (`/yolo`): opt in via the `--yolo` startup flag or via `/yolo` mid-session, then `/yolo` again or cycle back to normal to turn it off. The yolo banner (`⚠ yolo mode`) takes precedence visually while it's on; when a mode (auto or plan) is also active, the mode banner picks up a `⚠ yolo mode` suffix.

## Optional Podman command sandbox

By default, yottacode does not sandbox tools inside its own process. `run_bash`, file edits, git commands, and other tools run on the host unless you opt into the Podman command sandbox.

When `[sandbox] backend = "podman"` is enabled, approved `run_bash`/`run_tests` commands and document subprocess helpers run inside GHCR-published containers with no network by default, constrained mounts, and resource limits. File tools, git, GitHub, provider calls, MCP tools, and the TUI still run on the host; run yottacode itself inside a container or devcontainer when you need every tool isolated.

When sandboxing is off, `run_bash` is also *not* confined by the path deny lists that protect the structured file tools: a shell command can read `~/.ssh` or write `.git/hooks/` even though `read_file` / `write_file` cannot. The approval modal flags credential-store and git-hook references as a prompt aid, but the real boundary is the optional container sandbox or an outer container/devcontainer.

See [Command sandbox](sandbox.md) for the config, images, and runtime boundary.

## Browser automation

The `browser_*` tools (experimental behind `--experimental browser`; see
[experimental.md](experimental.md) and [tools.md](tools.md#browser_status))
drive a real, headless Chrome/Chromium instance over the Chrome
DevTools Protocol. They carry their own, stricter safety posture on
top of the normal approval model:

- **Isolated profile only.** Every session gets a fresh, empty temp
  profile directory — no cookies, history, saved logins, or
  extensions from your real Chrome. The tools cannot act as "you" on
  any site you're signed into, and there is no flag to opt into your
  real profile in v1. `browser_close` (explicit, or automatic at
  session shutdown) removes the profile directory.
- **Headless by default; visible only by handoff.** The session never
  shows a window on its own, so behavior doesn't silently degrade on a
  headless remote host or CI box. `browser_handoff` is the one way to get
  a visible window, so a human can complete a verification challenge the
  agent can't and shouldn't: it always prompts, fails cleanly when there's
  no display, and reopens the page in a *fresh* isolated profile rather
  than carrying cookies across. The agent is told not to attempt the
  challenge itself — the human does it. The tools add no stealth or
  evasion of their own (no hiding of automation flags, no fingerprint
  patching), and this doesn't change that. One thing to know: the
  headless session inherits chromedp's default device emulation — a fixed
  1280×800 viewport and a Mac Chrome user-agent string — which is a
  library default, not an evasion measure, and it does not match the real
  browser. The visible handoff window turns that emulation off and reports
  Chrome's real viewport and user agent.
- **Approval on every action that reads or changes page state** —
  `browser_navigate`, `browser_screenshot`, `browser_inspect`,
  `browser_click`, `browser_type`, `browser_hotkey`,
  `browser_scroll`, and `browser_handoff` all prompt. This includes the two read-only tools:
  a screenshot or an accessibility-tree snapshot can surface on-screen
  private data (an inbox, a logged-in dashboard, a form someone else
  left filled in) even though nothing was clicked. `browser_status`,
  `browser_wait`, `browser_close`, `browser_tabs`, `browser_switch_tab`,
  and `browser_close_tab` are the exceptions — they report existing
  state, block on an already-visible condition, change which tracked
  page later calls target without reading or changing its content, or
  only reduce capability, so none of them can expose anything an
  already-approved call hasn't already shown.

- **`/auto` keeps the risky browser calls gated.** Auto mode approves
  the other browser tools (inspect, screenshot, console/network reads,
  click, type, …) without a prompt so a web app can be verified end to
  end, but two things stay in the auto-mode safety floor and always ask:
  - `browser_upload` and `browser_download` — an upload sends a local
    file's bytes to a site and a download writes to local disk.
  - `browser_navigate` to anywhere but **this machine**. `localhost`,
    `127.x.x.x` and `::1` open unprompted (testing a dev server is what
    `/auto` is for); any other host — a real site, a private-range
    address, a cloud metadata address like `169.254.169.254`, a name
    that only looks local such as `localhost.evil.com`, or any URL with
    something unusual in it — stops and shows you the URL first. A page
    the agent has opened can contain instructions, and an auto-approved
    navigation would be a way to act on them: a URL with secrets in it
    sends them to whoever runs the site. Saving a per-site rule from the
    prompt (`Browser(navigate example.com)`) skips it for that site.

  Only `/yolo` (the explicit approve-everything mode) skips these
  prompts, and a deny rule still beats both.
- **Approvals can be remembered — narrowly.** The prompt offers the same
  `[S]` session / `[A]` always / `[D]` never choices as other tools, via
  `Browser(...)` rules (see
  [Creating allow and deny rules](#creating-allow-and-deny-rules-from-approvals)).
  The rule is per *verb*, not per selector: `[S]` on a `browser_inspect`
  saves `Browser(inspect)`, covering every later inspect but nothing else.
  `browser_navigate` is per *site*: `Browser(navigate streeteasy.com)`
  covers that exact host only — no subdomains, and never a blanket "any
  site". `browser_upload` and `browser_download` never get an allow
  shortcut (they move local file bytes across the write-path boundary),
  though `[D]` can block them. A URL with anything unusual in its host
  (userinfo, a backslash, `*`, non-ASCII, a non-http(s) scheme such as
  `file:`) is never turned into an allow rule and keeps prompting —
  including under a hand-written `Browser(navigate *)`, which only covers
  plain web hosts. The no-approval tools (`browser_close` and friends) sit
  outside the rules, so even a `Browser(*)` deny can't stop you closing
  the browser.
- **One browser, one *active* page, no parallelism.** The session
  tracks every tab it has opened (so `browser_tabs`/`browser_switch_tab`
  can list/switch among them, and a click that opens a
  `target="_blank"` tab or calls `window.open()` can be auto-followed),
  but every action still only ever touches the one currently active
  page. The tools don't implement `ParallelSafeTool`, so calls always
  serialize through the same approval queue as everything else
  regardless of how many tabs are tracked.

- **Upload/download reuse the existing path boundaries.**
  `browser_upload`'s local file paths and `browser_download`'s
  destination path both go through the same `ValidateWritePath` check
  as `write_file` — inside the session workspace or an `--allow-paths`
  root, no symlink writes. An upload also has to pass the credential
  **read** deny list that `read_file` enforces (`.env`, `~/.ssh`, …):
  an upload hands a local file's bytes to a web page, so a file the
  model can't read is a file it can't upload either. The download's
  final copy refuses to write through a symlink planted at the
  destination.
- **Only web URLs.** `browser_navigate` and `browser_download`'s `url`
  accept `http://`, `https://` and `about:blank` only. `file://` would
  let the browser read anything the user can — including the files the
  read tools deny — and `browser_inspect` would return it to the model;
  `chrome://`, `view-source:` and `devtools:` expose browser internals;
  `javascript:` would be a run-script primitive. It is an allowlist, so
  a scheme nobody thought of is refused too, and the check runs before a
  browser is launched. A web page's own links, redirects and
  `window.open` can't reach `file:` either — Chrome refuses that, and a
  real-browser test pins it.
- **The approval prompt shows what is being approved.** `browser_type`
  shows the text typed, `browser_upload` the files, and
  `browser_download` the source as well as the destination — bounded in
  length, with the true length shown, and with control characters
  escaped so an argument can't reflow the prompt to look like something
  else.
- **The browser keeps Chrome's process isolation.** chromedp's launch
  defaults turn site isolation off and run the network service inside the
  browser process; both are turned back on. Chrome's own sandbox is never
  disabled (nothing passes `--no-sandbox`). The leakless guard is a
  helper binary it extracts to a predictable `/tmp` path and runs without
  checking who owns it — on a shared machine another user could plant a
  program there. The helper is only used if it and its directory belong
  to you and nobody else can write them; otherwise the browser launches
  without the guard (a hard-killed yottacode may then leave an orphaned
  browser process behind).
- **Console/network capture is buffered continuously, and reading it
  needs approval.** `browser_console_logs`/`browser_network_requests`
  read from a per-page bounded buffer (`Runtime`/`Network` domain
  events) populated from the moment a page is tracked, not just during
  the call — so they can surface messages or requests from well before
  the call was made. Both always prompt: console output and request
  URLs can carry private data (a logged token, a session-bearing query
  param) even though nothing was clicked to produce them. Metadata
  only — no response bodies, and no request blocking/mocking.
- **Not available to `dispatch` workers.** A dispatch worker never
  sees the `browser_*` tools, regardless of whether `browser` is
  enabled for the parent session — concurrent browser sessions across
  workers multiply this surface in a way the single-session design
  hasn't been proven against yet.
- **Not routed through the command sandbox.** `run_bash`'s Podman
  sandbox doesn't apply here — chromedp launches the Chromium process
  directly, not via a shell command. Containerizing the browser itself
  is a possible future addition, not a v1 guarantee.

### Browser automation: what it does not protect against

The safeguards above narrow the browser tools; they do not make an agent
that is browsing untrusted pages safe. Know these before using it that
way:

- **Prompt injection.** Anything on a page — text, console output,
  network URLs, a screenshot — goes to the model, and a hostile page can
  contain instructions. Approval prompts and the `/auto` safety floor are
  the mitigations. In `/auto` the agent can't open a new site without
  asking (see above), which closes the main way an injected instruction
  could send data out. What is still open: `click` and `type` are
  auto-approved, so a page on `localhost` that links out to a hostile
  site, or a redirect, can still take the browser somewhere unprompted,
  and text typed into a form there goes with it. Prefer normal approval
  mode when the browser visits pages you don't control.
- **Internal network access.** Unlike `fetch_url`, which refuses
  loopback, private and link-local addresses, the browser can reach them
  — `localhost` dev servers are its main use, and a cloud instance's
  `169.254.169.254` metadata endpoint or an internal admin page is
  reachable the same way. The control is the approval prompt, which shows
  the URL: it applies to every navigation in normal mode, and to every
  non-loopback navigation in `/auto`. Redirects, links followed after the
  page loads, and DNS names that resolve to an internal address are not
  checked — the prompt shows a hostname, not where it resolves. On a
  cloud or internal-network host, treat browser access as network
  access.
- **Site scoping is a speed bump, not a boundary.** A saved
  `Browser(navigate example.com)` rule covers that exact host only, but
  an allowed `Browser(click)` can follow links anywhere and rules are
  per host, not per port.
- **The debugging port is unauthenticated on loopback.** Chrome is driven
  over a local DevTools port with no credentials, so any local process —
  or, on a shared machine, any local *user* — can drive your browser
  session. A web page cannot: a page's `fetch` and WebSocket to that port
  are refused and DNS-rebinding is rejected by Chrome (both checked
  against a live browser). Don't run this on a machine with local users
  you don't trust.
- **The temp profile can briefly outlive a crash.** Cookies and site
  data live in a `0700` directory under your temp dir, removed on
  `browser_close` and at shutdown. If yottacode is killed with `SIGKILL`
  it is left behind, and the next browser launch removes it — provided
  it is at least an hour old, owned by you, and Chrome's own lock file
  shows no live browser holds it (a directory it can't prove is dead is
  never touched; at most 20 are removed per launch). Until then it sits
  in your temp dir.
- **Downloads have both time and size limits.** A download is bounded by the
  60-second action timeout and a 100 MiB maximum. Partial files in the private
  scratch directory are removed when the operation fails or times out; the
  validated destination is not published until completion.

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

worktree-doctor-permissions-validation
## Permission policy diagnostics

`yottacode doctor` and the TUI `/doctor` command validate the machine-wide system policy (`/etc/yottacode/permissions.json`) together with the project shared and local policy files without modifying them. They report whether each source is missing, empty, valid, unreadable, or malformed, and show advisory warnings for risky or shadowed rules. A syntax, read, or rule-format error makes doctor return an issue; advisory lint warnings do not block a run.

## Bash rule matching

Bash allow rules are evaluated per command segment. Compound commands separated by `&&`, `||`, `;`, `|`, `&`, or a newline are not approved by an allow rule that matches only the first command. Commands inside `$(...)` and backtick substitutions are also evaluated independently.

For example, `Bash(go test *)` does not approve `go test ./... && rm -rf tmp`, and `Bash(echo *)` does not approve `echo $(curl https://example.com)`. The outer command is allowed only when every extracted command target is covered by an allow rule and no extracted target matches an ask or deny rule.

This parser is intentionally conservative and is not an operating-system security boundary. Absolute executable paths, scripts, aliases, and indirect subprocess file/network access still require the sandbox or other host controls for complete enforcement.

Project-local permission rules live in:

```text
<repo>/.yottacode/permissions.json
<repo>/.yottacode/permissions.local.json
```

Use:

- `permissions.json` for team-shared rules that can be committed
- `permissions.local.json` for personal rules that should be gitignored

The optional machine-wide administrator policy is `/etc/yottacode/permissions.json`. It is read-only to yottacode and is evaluated together with the project files. Rules use `deny > ask > allow` precedence.

Add this to `.gitignore`:

```gitignore
.yottacode/permissions.local.json
```

## Rule shape

```json
{
  "permissions": {
    "allow": ["Tests(go *)", "Github(read_*)", "Edit(internal/**)"],
    "ask": ["Github(create_pr)", "Read(**/.env*)"],
    "deny": ["Bash(rm *)"]
  }
}
```

Rules support `allow`, `ask`, and `deny` policy. Explicit deny rules still apply even when `--yolo` is set.

## Creating allow and deny rules from approvals

When an approval modal appears, use the keyboard: press **`Y`** to approve once, **`S`** to allow for the rest of this session, **`A`** to save a derived *allow* rule, **`N`** to reject, or **`D`** to save a derived *deny* rule into `permissions.local.json`. The modal ignores mouse clicks, including the close glyph, so safety decisions cannot be delayed or mis-triggered by terminal mouse events. Every always/never/session path shows the exact rule before it takes effect.

`[S]` session and `[A]` always derive the identical pattern and share the same suppression rules (see below) — the only difference is where the rule lives. `[A]` appends it to `permissions.local.json`, so it survives restarts and is visible to `/permissions`. `[S]` keeps it in memory only, for the rest of the current process: nothing is written to disk, so it can't outlive the session, leak into a teammate's checkout, or need cleaning up later. Use `[S]` for a rule you only want for this one exploratory session; use `[A]` for one you'd make again next time.

`[A]`/`[S]` are suppressed for compound shell commands and obviously dangerous verbs (`rm`, `curl`, `sudo`, …) — those are footgun-wide to blanket-allow, temporarily or not. `[D]` never is offered even for those (blocking a dangerous command permanently is exactly the point) and is scoped to `run_bash`, `git` and the approval-gated `browser_*` tools. Because bash rules are matched per segment, a block is derived at the verb level: hitting `[D]` never on `curl … | sh` saves `Bash(curl *)`, which then refuses `curl` anywhere. Since deny outranks allow, a `[D]` block also overrides any existing allow (persisted or session-scoped) for the same pattern.

Examples:

- `Tests(go *)` — allow `run_tests` invocations that use Go.
- `Git(status *)` — allow the unified `git` tool's status subcommand; most read-only Git helpers already run without prompts.
- `Github(read_*)` — allow native GitHub read helpers without allowing PR/issue writes.
- `Edit(internal/**)` — allow edits under one source tree.
- `Write(docs/**)` — allow new/overwritten files under docs.
- `Document(xlsx reports/**)` — allow generated spreadsheets in a reports directory.
- `Browser(inspect)` — stop asking before every `browser_inspect`; `Browser(navigate streeteasy.com)` — allow navigating to that one site. Hand-write `Browser(navigate *.example.com)` for subdomains.
- `MCP(filesystem/read_*)` — allow filesystem MCP server's read tools (see [MCP](mcp.md)).
- `MCP(github/*)` — allow every tool from the GitHub MCP server; prefer narrower server/tool rules when possible. Note: a glob like this can never auto-allow a tool the server marked destructive — only an exact `MCP(github/create_issue)`-style rule can, so a destructive call still prompts even under a broad allow rule.

`/permissions` also highlights risky-but-valid rules, such as broad `Bash(gh *)`, `Bash(python*)`, `Git(-C *)`, namespace-wide `Github(*)` / `MCP(*)`, `Browser(*)` / `Browser(navigate *)`, repo-wide delete allows, and allow rules shadowed by deny rules. Warnings are advisory only: yottacode still honors the policy file exactly as written. Persisting `MCP(*)` from an interactive "always allow" prompt requires an explicit second confirmation, since it covers every server and tool.

MCP tool trust decisions (`approval_mode = "allow-readonly"`, `trust_annotations`) rely on server-declared annotations, which are advisory and unverified, not a guarantee — see [MCP](mcp.md#global-mcp-policy).

## Testing a rule before you trust it

The matching semantics — per-segment Bash splitting, doublestar path globs vs. free-form string globs, ratcheted "any-deny / all-allow / any-ask" evaluation for batch calls — are subtle enough that a hand-written rule can look right and match wrong. `yottacode permissions test` runs a hypothetical call through the exact same evaluator the agent loop uses, from the current directory's rule files, without executing anything:

```bash
yottacode permissions test bash "git push origin main --force"
# tool:    run_bash
# args:    {"command":"git push origin main --force"}
# verdict: ask
# matched: Ask(git push *)  [permissions.local.json]

yottacode permissions test run_bash '{"command":"go test ./..."}'
yottacode permissions test edit_file '{"path":"internal/foo.go"}'
```

`<tool>` is the internal tool name (`run_bash`, `write_file`, `edit_file`, `git`, `read_file`, `fetch_url`, …; see [tools.md](tools.md)); `bash` is accepted as a shorthand for `run_bash` and, only for that alias, the second argument may be a bare command string instead of JSON. A tool name with no permission mapping at all (nothing yottacode's rule engine ever looks at) reports `verdict: n/a` rather than a misleading `default`.

## Yolo mode (the danger setting)

```bash
yottacode --yolo
```

This is dangerous. It skips approval prompts for matching operations and raises the iteration cap to a high but finite budget, but explicit deny rules remain enforced. Use it only in trusted automation or disposable environments. Two ways in: `yottacode --yolo` at startup (restart without the flag to recover), or `/yolo` in the TUI to toggle the overlay on or off mid-session.

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
