# Browser automation (experimental)

The `browser_*` tools drive an isolated, disposable system Chrome/Chromium instance over CDP. The feature remains experimental and must be explicitly enabled with `--experimental browser`, `YOTTACODE_EXPERIMENTAL=browser`, or config. It does not download browsers, add networking, or change sandbox policy.

`browser_status` is read-only and never launches a process. It reports discovered binary path, active state, headless/headed mode, current URL, tab count, and isolated profile directory. A missing binary, launch failure, dead process, canceled action, blocked URL, selector timeout, tab lookup failure, download timeout/size rejection, or missing display is reported with a stable browser error category and an actionable message. A dead process is cleaned up and the next action may relaunch; `browser_close` remains terminal.

## Human verification and handoff

The browser never solves CAPTCHAs, bypasses bot checks, uses stealth, or persists profiles. `browser_handoff` only opens the same isolated flow in a visible window so a human can complete verification. It requires a local display (`DISPLAY` or `WAYLAND_DISPLAY`); SSH, containers, and headless CI should instead ask the user to complete the step in their own browser and provide the result. Handoff uses a fresh isolated profile and carries only the URL.

## Non-goals and limits

- No local-file, `chrome://`, `javascript:`, or `data:` navigation; only HTTP(S) and `about:blank`.
- No sandbox or network-policy changes, proxying, stealth, CAPTCHA bypass, or persistent login profiles.
- No response bodies, iframe targeting, device emulation, drag-and-drop, or default visible window.
- Downloads are temporary, cleaned after success/failure, and rejected above the fixed safety limit.

## Verification

Run the standard checks before changing browser code:

```sh
gofmt -w internal/browser/*.go
go test ./...
go test -race ./...
go vet ./...
go test -tags integration ./internal/browser/... -count=1
```

The integration command requires a system Chrome/Chromium. CI runs the tagged suite on Linux under Xvfb and on macOS; environments without a browser/display skip or report the corresponding handoff coverage rather than enabling new capabilities.

See [tools.md](tools.md#browser_status), [experimental.md](experimental.md), and [security-and-allow-lists.md](security-and-allow-lists.md#browser-automation) for the tool and safety contracts.
