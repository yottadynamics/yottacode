# Browser automation (experimental)

The `browser_*` tools drive a real Chrome/Chromium instance (headless
by default — see [When a site asks for human verification](#when-a-site-asks-for-human-verification))
over the Chrome DevTools Protocol (`go-rod/rod`, no Node.js or
Playwright). Once enabled, the agent can open pages, click and type,
read what's on screen, follow popups, upload and download files, and
read a page's console output and network activity — all in an
isolated, disposable browser profile that's never your real,
logged-in Chrome.

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

## When a site asks for human verification

The browser is headless, so you can't see it — and some sites (rental
listings, ticketing, retail) answer an automated client with a
"Press & Hold to confirm you are a human" box or a CAPTCHA. The agent
can't and won't get past that on its own. Instead it calls
`browser_handoff`, which (after you approve it) reopens the same
isolated browser as a visible window on the page it was stuck on:

1. The agent tells you a verification is needed and calls
   `browser_handoff`.
2. A Chrome window opens on your desktop. Complete the check yourself.
3. Tell the agent you're done; it confirms the real page loaded and
   carries on in that same (now visible) session.

A check completed in your *normal* browser does not help: the agent's
browser is a separate, isolated profile that shares no cookies with it,
so the site still sees the agent's browser as unverified. The check has
to be completed in the window `browser_handoff` opens — that window *is*
the agent's browser. (Completing it in your own browser is still useful
if you paste the page's content back to the agent yourself.)

Things to know: the window is a **fresh** isolated profile, so anything
the headless session had done (a login, a filled form) is not carried
over — only the URL is. It needs a display, so over SSH or in a
container it fails and the agent will ask you to open the page in your
own browser and paste what it needs instead. And a site's bot detection
can still decide to challenge an automation-controlled Chrome again;
nothing here tries to evade that.

## What this can't do (yet)

Scoped deliberately, not accidentally missing:

- **No local files or `chrome://` pages.** Only `http://`, `https://`
  and `about:blank` can be opened, so the browser can't be used to read
  local files around the read deny list. To check a local build, serve it
  (`python3 -m http.server`, your dev server) and open the
  `http://localhost` URL.
- **No visible browser window by default.** Headless unless you're
  handed the session with `browser_handoff` (see above) — otherwise you
  only see its output (screenshots, text, logs).
- **No persistent login across sessions.** Every session starts from a
  fresh, empty profile; sign-in flows have to be driven fresh each time.
- **No response bodies from `browser_network_requests`** — method,
  URL, status, and failures only, not the actual response content.
- **No iframe targeting, viewport/device emulation, or drag-and-drop.**
  Selectors target the top-level document; there's one fixed viewport
  size and no synthesized drag gestures.

See [experimental.md](experimental.md) for the full flag catalog and
[security-and-allow-lists.md](security-and-allow-lists.md#browser-automation)
for exactly what needs approval and why.
