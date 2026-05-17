---
name: webapp-testing
description: End-to-end browser testing of a local webapp using a headless browser. Use when the user asks to "test the UI", "click through the form", "take a screenshot of the new page", or any task that requires driving an actual browser against a running web app.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/anthropics/skills (webapp-testing)
---

# Webapp testing

End-to-end UI verification needs an actual browser. The agent does not have a built-in browser driver — drive Playwright (or Puppeteer, or Cypress) via `run_bash`, capture the result, and report back.

## Prerequisites

Before you can run a browser test, the workspace needs:

1. A test runner installed (Playwright recommended: `npm install -D @playwright/test && npx playwright install chromium`). If not installed, ask the user to install it first; this skill is about driving the runner, not setting up the toolchain.
2. The webapp running locally on a known port. Start it yourself only if the user has already set up `npm run dev` (or equivalent) and asked you to. Otherwise ask.

State the prerequisites you confirmed. If any are missing, stop and ask before continuing.

## A complete first run

The smallest useful loop:

```bash
# 1. Start the app in background (user already running it? skip)
npm run dev &
APP_PID=$!
sleep 3  # wait for the listener

# 2. Write a one-off test
cat > /tmp/test-one.spec.ts <<'EOF'
import { test, expect } from '@playwright/test';

test('homepage renders', async ({ page }) => {
  await page.goto('http://localhost:3000');
  await expect(page.locator('h1')).toContainText('Welcome');
});
EOF

# 3. Run it
npx playwright test /tmp/test-one.spec.ts --reporter=line

# 4. Clean up
kill $APP_PID
```

Capture the exit code; non-zero = failure. Include the test output in your reply so the user can see what failed.

## Common operations

| Goal | Playwright snippet |
|---|---|
| Click a button | `await page.getByRole('button', { name: 'Submit' }).click();` |
| Fill a form | `await page.getByLabel('Email').fill('user@example.com');` |
| Wait for navigation | `await page.waitForURL('**/dashboard');` |
| Assert visible text | `await expect(page.getByText('Hello')).toBeVisible();` |
| Wait for an element | `await page.locator('[data-testid="foo"]').waitFor({ state: 'visible' });` |
| Take a screenshot | `await page.screenshot({ path: '/tmp/shot.png', fullPage: true });` |
| Capture console logs | `page.on('console', m => console.log('[browser]', m.text()));` |
| Network mock | `await page.route('**/api/foo', r => r.fulfill({ json: {...} }));` |

Prefer **role-based locators** (`getByRole`, `getByLabel`, `getByText`) over CSS selectors. They survive refactors and read like user actions.

## Screenshot-driven debugging

When a flow breaks at step N, take a screenshot at each step:

```typescript
test('checkout', async ({ page }) => {
  await page.goto('/cart');
  await page.screenshot({ path: '/tmp/01-cart.png' });
  await page.getByRole('button', { name: 'Checkout' }).click();
  await page.screenshot({ path: '/tmp/02-checkout.png' });
  await page.getByLabel('Card number').fill('4242 4242 4242 4242');
  await page.screenshot({ path: '/tmp/03-filled.png' });
});
```

Then read the screenshots back with `read_file` — they render in the conversation as actual images. The screenshot is often the answer ("the button isn't where you said it was").

## Network and console capture

If the test fails on an interaction, the browser's console + network logs usually explain why:

```typescript
test('with logs', async ({ page }) => {
  page.on('console', m => console.log(`[console:${m.type()}] ${m.text()}`));
  page.on('pageerror', e => console.log(`[error] ${e.message}`));
  page.on('requestfailed', r => console.log(`[net-fail] ${r.url()} ${r.failure()?.errorText}`));
  // … your test
});
```

A 401 on `/api/me` or an `Uncaught TypeError` will surface in the test output.

## Headed vs headless

- **Headless** (default) — fastest, no display required. Use for CI and the agent's runs.
- **Headed** (`--headed`) — opens a visible browser. The agent can't see it; only the user can. Useful when you ask the user to watch a flow.
- **Trace viewer** — `--trace on` produces a `.zip` with frame-by-frame DOM snapshots. The user can open it with `npx playwright show-trace trace.zip` to debug timing issues.

## Cleanup

Always stop processes you started: `kill $APP_PID` for the dev server, `rm /tmp/test-*.spec.ts` for one-off tests, `rm /tmp/*.png` for throwaway screenshots once you've shown them to the user.

## Never

- Hit production URLs. If the user's `package.json` points `npm run dev` at a real domain, refuse and ask.
- Bypass authentication by hard-coding tokens. Use the app's own login flow with test credentials.
- Disable test failures (`test.skip`, `--shard=0/N` with N higher than the test count). If a test is wrong, fix the test or remove it deliberately, not silently.
- Take screenshots of pages with real user data without asking. The screenshot becomes part of the conversation.
