# Browser automation (experimental)

The `browser_*` tools drive an isolated, disposable system Chrome/Chromium instance over CDP. The feature remains experimental and must be explicitly enabled with `--experimental browser`, `YOTTACODE_EXPERIMENTAL=browser`, or config. It does not download browsers, add networking, or change sandbox policy.

`browser_status` is read-only and never launches a process. It reports discovered binary path, active state, headless/headed mode, current URL, tab count, and isolated profile directory. A missing binary, launch failure, dead process, canceled action, blocked URL, selector timeout, tab lookup failure, download timeout/size rejection, or missing display is reported with a stable browser error category and an actionable message. A dead process is cleaned up and the next action may relaunch; `browser_close` remains terminal.

Run `yottacode doctor` with the feature enabled to see a **Browser** section: the binary found, its version (warning below Chrome 120, a conservative floor: older builds are untested), whether a visible window is possible, and whether the host will stop Chrome from starting (see [Running as root or in a container](#running-as-root-or-in-a-container)).

## Selectors

Every tool that takes a `selector` accepts three forms:

| Form | Example | Use |
|---|---|---|
| CSS | `#login`, `button.primary` | Anything you can name |
| Element ref | `ref=e12` | The exact node `browser_inspect` described |
| Iframe path | `iframe#pay >>> #card` | An element inside an iframe; chain `>>>` for nested frames |

`browser_inspect` tags interactive elements (buttons, links, inputs, dropdowns, tabs, …) with `[ref=eN]`. Refs remove the main source of wrong clicks — a CSS selector guessed from a text snapshot. A full-page inspect starts a fresh numbering, so an old ref can never silently point at a different element; refs also stop working after a navigation, and an unknown or stale ref fails with a message telling the agent to inspect again. To resolve a ref the browser sets a `data-yottacode-ref` attribute on that element. Refs address the top document only; use a CSS selector after `>>>`.

Same-process iframes work as shown. A frame or `<select>` that doesn't appear fails after 10 seconds (a typo shouldn't cost the whole 60-second action timeout).

## Network policy

`browser_navigate` only vets the URL the agent types. Once a page is loaded, *its own* requests — redirects, images, scripts, `fetch`/XHR, iframes, WebSockets — are checked one by one before they leave the browser, so a page cannot use the browser to reach your machine or network:

| Destination | Reachable? |
|---|---|
| Public addresses | Yes |
| Link-local and cloud metadata (`169.254.0.0/16`, `fe80::/10`, `metadata.google.internal`, `100.100.100.200`, `168.63.129.16`, …) | **Never**, even by explicit navigation |
| Loopback (`localhost`, `127.0.0.0/8`, `::1`) | Only from a page that is itself on loopback, which the agent reached by navigating there explicitly |
| Private network (RFC 1918, CGNAT, ULA, `0.0.0.0`) | Only from a page on that same host, which the agent reached by navigating there explicitly |
| Anything but http(s)/ws(s)/data/blob/about | No |

**Local access is not a standing grant.** Navigating to `http://localhost:3000` lets that page (and the API calls it makes) reach loopback, but the moment the browser goes to a public site that permission is gone: the public page cannot probe your dev server, your debugger port or your LAN, and a redirect chain that passes through a public host cannot land back on localhost. Each tab is judged by where it is: with a dev tab and a public tab open, only the dev tab keeps local access. A cross-site iframe or a worker takes the location of the page that owns it, so a public page's frames and workers get none; a worker with no page above it (a shared or service worker) gets none either. Coming back to a local page is an explicit navigation again.

Hostnames are resolved before the check, so a public-looking name that points at `10.0.0.5` or `169.254.169.254` is caught. A refused request fails with `net::ERR_BLOCKED_BY_CLIENT`, shows up in `browser_console_logs` as a `[blocked]` entry with the reason, and — when it is what failed a navigation — is returned as the navigation's error.

The check covers **every target**, not just the page you are looking at: cross-site iframes (which Chrome runs in separate processes), popups and workers included. A small guard (`internal/browser/guard.go`) keeps its own DevTools connection, asks Chrome to attach to each new target *paused*, installs request interception on it, and only then lets it run — so even the first script of an iframe cannot make a request that skips the check. If the guard cannot start, the session does not launch; if its connection drops later, the browser is stopped immediately (pages already open do not keep running unvetted) and the next action relaunches a guarded one. What it does not do: it vets the destination host, not the request path or body, and it does not rewrite DNS (DNS rebinding: a name the page controls that answers differently at check time and at connect time could still slip through; a lookup that fails is never remembered as "public", but that does not close the race).

## Untrusted page content

Everything page-derived that a tool returns — the accessibility tree, tab titles, console output, request URLs, response bodies, the page title in `browser_navigate` — is wrapped in an `untrusted_web_content` envelope with a notice that it is data, not instructions. Copies of the envelope tag inside the page text are neutralized so a page cannot close the envelope early and continue "outside" it. Output is capped (40,000 characters per tool result, 2,000 per console message or request URL). This is a mitigation, not a guarantee: approvals for `browser_click`/`browser_type` stay on by default, and `/auto` keeps `browser_navigate` to non-local hosts, `browser_upload` and `browser_download` gated.

## Dialogs

JavaScript dialogs have no tool surface, so a fixed policy answers them and records each in `browser_console_logs` (`[dialog]`): `alert` and `beforeunload` are **accepted** (they only inform, and refusing `beforeunload` would block navigation); `confirm` and `prompt` are **cancelled** (accepting would answer a question on your behalf).

## Human verification and handoff

The browser never solves CAPTCHAs, bypasses bot checks, or persists profiles. `browser_handoff` only opens the same isolated flow in a visible window so a human can complete verification. It requires a local display (`DISPLAY` or `WAYLAND_DISPLAY`); SSH, containers, and headless CI should instead ask the user to complete the step in their own browser and provide the result. Handoff uses a fresh isolated profile and carries only the URL.

When the user says they're done, the agent calls `browser_handoff` with `action: "resume"`: it confirms the window is still open and reports the page it is on and the tab count — without relaunching, so cookies and the passed verification stay intact — then checks the page with `browser_inspect` before continuing. Resume needs no approval; opening the window does.

Every session (headless and headed) does suppress the automation signals a stock headless Chrome volunteers for free — `navigator.webdriver`, the `HeadlessChrome` user agent/client hints, and headless Chrome's small default window size — so a page doesn't get flagged as a bot purely for looking like an out-of-the-box automation rig. This is cosmetic self-consistency (`internal/browser/stealth.go`), not evasion: no JavaScript is injected, nothing the browser reports is falsified beyond replacing "Headless" in its own version strings, and it does not affect whether a real challenge fires or how it's resolved.

## Subagents and dispatch

The session has one browser and one active tab. `dispatch` workers and **background subagents never get `browser_*` tools**: an unattended child would drive the parent's page, and `browser_close` from it would end the parent's session. A foreground subagent shares the parent's browser while the parent waits, so there is no concurrent use.

## Limits

| Limit | Value |
|---|---|
| Tabs per session | 8 (popups beyond that are closed and noted in the console log) |
| Buffered console / network entries | 200 per tab; each console message or URL capped at 2,000 characters |
| Tool result size (page-derived text) | 40,000 characters |
| Download | 100 MB |
| Upload | 100 MB total, regular files only |
| Response body (`browser_response_body`) | 32 KB default, 40,000 bytes maximum per call; larger bodies are read in pieces with `offset`; text only |
| Per-action timeout | 60 seconds |

## Running as root or in a container

Chrome refuses to start its sandbox as uid 0, and yottacode deliberately does not pass `--no-sandbox`. As root (typical in a Docker image) launch fails; the error and `yottacode doctor` both say so. Run yottacode as a non-root user. Inside a container, Chrome's sandbox also needs user namespaces; if launch still fails as a non-root user, adjust the container's seccomp/user-namespace settings rather than disabling the sandbox. Handoff needs a display and is unavailable in containers and over SSH.

## Non-goals

- No local-file, `chrome://`, `javascript:`, or `data:` navigation; only HTTP(S) and `about:blank`.
- No proxying, CAPTCHA-solving, or persistent login profiles. No sandbox or network-policy changes outside the browser.
- No device emulation, drag-and-drop, request mocking, or default visible window.
- Downloads are temporary, cleaned after success/failure, and rejected above the fixed safety limit.

## Verification

Run the standard checks before changing browser code:

```sh
gofmt -w internal/browser/*.go
go test ./...
go test -race ./...
go vet ./...
go test -tags integration ./internal/browser/... -count=1
go test -tags integration ./internal/agent/ -run TestIntegration_HostilePage -count=1
```

The integration commands require a system Chrome/Chromium. CI runs the tagged suites on Linux under Xvfb and on macOS; environments without a browser/display skip or report the corresponding handoff coverage rather than enabling new capabilities. The security regression set is: URL-scheme refusals, redirect and subresource blocking toward metadata and the private network, the prompt-injection envelope corpus, and a guard that the package never writes to stderr (which would corrupt the TUI).

See [tools.md](tools.md#browser_status), [experimental.md](experimental.md), and [security-and-allow-lists.md](security-and-allow-lists.md#browser-automation) for the tool and safety contracts.
