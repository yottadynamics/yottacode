# Browser automation (experimental)

The `browser_*` tools drive a real, headless Chrome/Chromium instance
over the Chrome DevTools Protocol (`go-rod/rod`, no Node.js or
Playwright). Once enabled, the agent can open pages, click and type,
read what's on screen (including inside iframes), follow popups, upload
and download files, run JavaScript, and read a page's console output and
network activity — all in an isolated, disposable browser profile that's
never your real, logged-in Chrome. The browser can also be a rented
cloud browser or a self-hosted Camofox server; see [Backends](#backends).

> **Status: experimental.** Enable with `--experimental browser`,
> `YOTTACODE_EXPERIMENTAL=browser`, or `[experimental] browser = true`
> in config. See [tools.md](tools.md#browser_status) for the full
> per-tool parameter reference and
> [security-and-allow-lists.md](security-and-allow-lists.md#browser-automation)
> for the safety model (approval gating, the isolated-profile guarantee,
> write-path validation for upload/download).

## What you can use this for

- **Debugging a running web app.** Point the agent at `localhost:3000`
  and describe the symptom ("the login button does nothing") — it can
  read the page, click through the flow, and check `browser_console_logs`/
  `browser_network_requests` for the JS error or failed API call that's
  actually causing it, often without you having to open DevTools
  yourself.
- **Verifying a frontend change actually works**, not just that it
  compiles — navigate to the dev server, exercise the changed feature,
  screenshot the result, before calling a task done.
- **Driving a form or a multi-step flow** (signup, checkout, a wizard)
  to confirm it behaves end to end, including flows that open a new
  tab partway through (OAuth popups, "open in new window" links).
- **Pulling structured info off a page** you'd otherwise have to read
  yourself — an internal dashboard, a status page, a table with no API.
- **Exercising upload/download UI** — confirming a file picker accepts
  the right files, or that a "Download CSV" button produces a file with
  the content you expect.
- **A lightweight smoke test after a build** — navigate, click through
  primary nav, and report any console errors or failed requests, as one
  scripted pass instead of manually clicking around.

## How the agent targets elements

`browser_inspect` returns an accessibility-tree outline of the page in
which every interactive element — button, link, textbox, checkbox,
combobox, tab, … — is tagged with a short ref:

```
@e1 textbox "Email"
@e2 checkbox "I agree" [checked]
@e3 button "Sign in"
@e4 button "Cancel" [disabled]
```

Every tool that takes an element (`browser_click`, `browser_type`,
`browser_scroll`, `browser_wait`, `browser_screenshot`, `browser_upload`,
`browser_download`, and a scoped `browser_inspect`) accepts an `@eN` ref
wherever it accepts a CSS selector, so the agent clicks what it *saw*
instead of guessing a selector from a tree that carries no ids or
classes. A few things worth knowing:

- **Refs are valid until the next whole-page `browser_inspect`.** That
  call replaces them. A ref whose element is gone — the page navigated,
  or the node was removed — fails as *stale* with a message to inspect
  again, never silently clicking something else. A *scoped* inspect
  (with a `selector`) adds to the existing refs instead of replacing
  them.
- **`interactive_only`** lists just the controls, flat — the compact
  view for "what can I click?" — instead of the full outline with
  headings and text.
- **Iframes are included.** Controls inside same-origin and cross-origin
  iframes are listed under a `--- frame "name" url ---` header with
  refs of their own, and click/type work on them the same way. This holds
  whether or not the browser runs with site isolation: when a cross-site
  frame gets its own process, it is found by auto-attaching to it and
  driven through its own DevTools session. In that case only
  `browser_click` and `browser_type` work on its controls (the other
  element tools answer "not supported inside a cross-process iframe"),
  and an annotated screenshot leaves its controls unboxed — a box drawn
  from the frame's own coordinates would land in the wrong place.
- **Big pages aren't silently cut.** A snapshot over the display budget
  ends in `…[truncated]` *and* is saved in full to a file whose path is
  returned (read it with `read_file`); refs past the cut still work.
  These files live in a per-session scratch directory that's removed
  when the browser closes.
- **`browser_screenshot` with `annotate`** draws a numbered box over
  every control — box *N* is `@eN` — so a vision model can act on what
  it sees. It refreshes the refs like a whole-page inspect, and the
  overlay is removed again before the tool returns.

## Running JavaScript and handling dialogs

`browser_eval` evaluates a JavaScript *expression* in the page, like
typing it in the DevTools console, and returns the result as JSON — the
way to pull structured data the accessibility tree doesn't show (a
table's cells, a global, `localStorage`). A returned Promise is awaited,
a runaway script is cut off after 15 seconds, and output is truncated at
20,000 characters. Every call asks for approval, and it never gets an
"always allow" shortcut: page JavaScript can read anything the page's
origin can, including cookies and storage.

Native dialogs (`alert`, `confirm`, `prompt`, `beforeunload`) block a
page until answered, so they are answered automatically — **dismissed by
default**, so a `confirm("Delete everything?")` returns `false`. Every
dialog is recorded (type, message, how it was answered) and shows up in
`browser_console_logs`. To let one through, the agent calls
`browser_dialog` with `accept` (and `prompt_text` for `prompt()`) *before*
the click that triggers it; that policy applies until changed, and
choosing `accept` asks for approval. `browser_back` goes back one page
in the tab's history.

## Examples

These are realistic prompts you could hand the agent once `browser` is
enabled. Each one chains several `browser_*` tools together; you don't
need to name the tools yourself — the agent picks the sequence.

### "Why doesn't the login form work?"

> Navigate to http://localhost:3000/login, fill in test@example.com /
> password123, submit, and tell me what's actually going wrong.

The agent navigates, types into the two fields, submits (`browser_type`
with `submit: true`), then — since the visible symptom alone rarely
says *why* — checks `browser_console_logs` for a thrown exception and
`browser_network_requests` for the login POST's actual status. This is
usually the fastest path to a root cause: a 401 with a specific error
body, or a `TypeError` from a null field, tells you immediately what to
fix in the source, before you've read a single line of application
code yourself.

### "Confirm my change to the checkout flow works"

> I just changed how the discount code field validates. Start the dev
> server if it's not running, go to /cart, add an item, apply code
> SAVE10, and confirm the total updates correctly.

A concrete "close the loop" check after an edit — click through the
real flow, `browser_screenshot` or `browser_inspect` the resulting
total, and report back with evidence instead of "should work now."

### "Test that the file upload actually accepts CSVs"

> On the /import page, upload testdata/sample.csv through the file
> picker and confirm the preview table shows the right row count.

`browser_upload` sets the file input directly (works even on the
common `display:none` + styled-button pattern), then `browser_inspect`
or a `browser_wait` on the preview element confirms the app actually
processed it — a fast way to test upload handling without leaving the
terminal.

### "Download the report and check its contents"

> Click "Export as CSV" on the /reports page and get me the file.

`browser_download` clicks the export button, saves the resulting file
to a path you choose, and reports its size and the site's suggested
filename — hand that path to `read_document` next if you want the
agent to check the actual contents.

### "Walk through the OAuth sign-in and confirm we land back correctly"

> Click "Sign in with Google" and confirm the app redirects back to
> /dashboard afterward (I'm already logged into a test Google account
> in the isolated browser profile).

A click that opens a new tab is auto-followed (`browser_click` follows
`target="_blank"` links and `window.open()` within a short detection
window), so the agent's next actions already target the popup. Once
the flow completes and the popup closes itself, the active tab falls
back to the original page automatically — `browser_tabs` at any point
shows what's open and which tab is active if you want to double-check.

### "Smoke-test the site after this deploy"

> Navigate to the homepage and click through the main nav links (About,
> Pricing, Docs). Tell me about any console errors or failed requests
> you see along the way.

A repeatable pass an agent can run standalone: navigate, click, check
`browser_console_logs`/`browser_network_requests` after each stop,
report anything that looks wrong. Useful as a "did I just break
something obvious" pass before or after a deploy.

## Backends

By default the browser is a system Chrome/Chromium that yottacode
launches as a child process. Three options in the `[browser]` block of
`config.toml` change what drives it (all inert unless the `browser`
experimental feature is on; see
[configuration.md](configuration.md#browser)):

```toml
[browser]
provider = "local"       # "local" (default) | "browserbase" | "camofox"
stealth = false          # local only
proxy_url = ""           # local only, e.g. "http://user:pass@proxy.example:3128"
idle_timeout_minutes = 15
```

| | `local` | `browserbase` | `camofox` |
|---|---|---|---|
| Where pages are processed | this machine | Browserbase's cloud | your Camofox server |
| Engine | Chrome/Chromium (CDP) | Chrome (CDP) | Firefox fork (REST) |
| Refs, `interactive_only`, iframes | yes | yes | server's own snapshot, refs only |
| `browser_eval`, console, network | yes | yes | no |
| Dialogs, annotated screenshots | yes | yes | no |
| Tabs (switch/close) | yes | yes | one tab |
| `browser_upload` / `browser_download` | yes | **no** (no shared filesystem) | **no** |
| CSS selectors | yes | yes | **no** — refs only |
| `browser_wait` | selector / text / idle | selector / text / idle | text only |

A tool the active backend can't do fails with an explicit "not supported
on a remote browser session" error rather than doing something else.

### Stealth and a proxy (local)

`stealth = true` launches Chrome without the flags that announce
automation (`--enable-automation`, Blink's `AutomationControlled`) and
presents a self-consistent identity: a user agent (set at launch, so
popups and frames have it from their first request), client hints,
platform and language list that match the real Chrome build and this OS,
instead of the fake Mac laptop on an old Chrome that automation defaults
to. It hides the *obvious* tells — it is not a guarantee against serious
bot detection, and it never solves a challenge for you.

It deliberately injects **no JavaScript**. The popular evasion bundles
(puppeteer-extra's, which a Go wrapper republishes) patch properties from
inside the page, and on a current Chrome they changed nothing useful once
the launch flags and client hints were right — while introducing
contradictions of their own (a Mac GPU string under a Linux user agent, a
notifications permission query that disagrees with
`Notification.permission`). Patching by hand also has to chase every
Chrome release, which is how such scripts go stale. What can be configured
truthfully is; what can't stays honest — headless Chrome still reports
software-rendered WebGL and a small screen, and a check that looks for
those will see them.

`proxy_url` routes the browser through an `http://`, `https://` or
`socks5://` proxy. Credentials in an `http(s)` proxy URL answer the
proxy's 407 challenge automatically (and only the proxy's — never a
site's own login prompt); Chrome cannot authenticate to a SOCKS proxy.
Chrome bypasses the proxy for `localhost`, so dev servers keep working.
With proxy credentials set, every request is intercepted to supply them,
which costs one extra round-trip per request.

### Browserbase (cloud)

`provider = "browserbase"` rents a cloud browser from
[Browserbase](https://browserbase.com) over CDP. Stealth, residential
proxies and CAPTCHA solving are *Browserbase's* server-side features:
yottacode only asks for a session with those flags set — it implements
no evasion or solver of its own, and what you get depends on your
Browserbase plan.

```toml
[browser]
provider = "browserbase"

[browser.browserbase]
project_id = "…"               # or $BROWSERBASE_PROJECT_ID
# api_key_env = "BROWSERBASE_API_KEY"   # the key is read from this env var, never from the file
# no_proxies = false           # residential proxies are on by default (and billed)
# no_keep_alive = false        # reconnect after a dropped connection
# advanced_stealth = false     # Browserbase's custom Chromium (Scale plan)
# session_timeout_seconds = 0  # 0 = project default, max 21600
```

Things to know before turning it on:

- **Page content leaves your machine.** Every page the agent opens is
  rendered and processed by a third party. That's the opposite of the
  local default — keep it off for anything sensitive. Each navigation
  result says `[remote browser: browserbase]`, and `browser_status`
  reports the provider.
- **Sessions bill until released.** yottacode releases the session on
  `browser_close`, on session exit, on idle reap, on a crash, and
  immediately if the connection fails after the session was created.
- **A plan without keep-alive or proxies degrades, it doesn't fail.**
  On HTTP 402 the session is retried without keep-alive, then without
  proxies; `browser_status` says what was dropped.
- **No file transfer.** CDP file paths name the *browser's* filesystem,
  so `browser_upload`/`browser_download` are refused.
- **Bypassing a site's bot checks may violate its terms.** Accounts can
  be banned for it. That is your call to make; it is why nothing here is
  on by default.

### Camofox (self-hosted)

`provider = "camofox"` drives a
[Camofox](https://github.com/jo-inc/camofox-browser) server — a Firefox
fork with fingerprint spoofing built in, wrapped in a REST API you run
yourself (typically in Docker):

```toml
[browser]
provider = "camofox"

[browser.camofox]
url = "http://localhost:9377"   # or $CAMOFOX_URL
# api_key_env = "CAMOFOX_API_KEY"   # optional bearer token, read from this env var
```

It isn't CDP, so it's a smaller surface (see the table): elements are
targeted by the server's own refs only, there's no JavaScript evaluation,
console or network capture, and one tab. URLs are opened *from the
server's point of view* — if it runs in Docker, `localhost:3000` is the
container, not your machine.

### Idle browsers

A browser that sits unused for `idle_timeout_minutes` (default 15;
negative disables) is closed, so a long session doesn't keep a headless
Chrome running for hours. The next action launches a fresh one — with a
fresh, empty profile, so open tabs and page state are gone — and
`browser_status` says when that happened.

## What this can't do (yet)

Scoped deliberately, not accidentally missing:

- **No visible browser window.** Headless only — you won't see it
  running, only its output (screenshots, text, logs).
- **No persistent login across sessions.** Every session starts from a
  fresh, empty profile; sign-in flows have to be driven fresh each time.
- **No response bodies from `browser_network_requests`** — method,
  URL, status, and failures only, not the actual response content.
- **No viewport/device emulation or drag-and-drop.** There's one fixed
  viewport size and no synthesized drag gestures.
- **No raw CDP passthrough.** A generic "send any DevTools command" tool
  would bypass the per-site navigation approvals and the write-path
  checks the curated tools enforce.
- **Cloud metadata endpoints are refused** (`169.254.169.254`,
  `metadata.google.internal`, and the rest of the link-local range),
  even under `--yolo`, whether reached by URL, redirect, or a page's own
  requests — and a hostname that *resolves* to one is refused for a direct
  navigation. That is a guard against the attempts a page or a
  prompt-injected model would make, not a network boundary: a redirect
  through a DNS alias, DNS rebinding, and web-worker requests are not
  caught (details in
  [security-and-allow-lists.md](security-and-allow-lists.md#browser-automation)).
  Private and loopback addresses are *not* blocked — `localhost:3000` is
  the main use case.

See [experimental.md](experimental.md) for the full flag catalog and
[security-and-allow-lists.md](security-and-allow-lists.md#browser-automation)
for exactly what needs approval and why.
