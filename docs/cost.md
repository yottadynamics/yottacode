# Usage and cost (`/usage`)

`/usage` shows how many tokens the current session has spent — per
model, plus a rolling total across every session created today — along
with live rate-limit headroom and a provider-aware account block.

**It does not show a dollar estimate.** Token *counts* are reported by
the provider and are exact, but a dollar *figure* would require a price
per model, and no provider exposes per-model pricing through the
inference API. The only way to price tokens is a hand-maintained table
that drifts the moment a provider changes rates or ships a new model —
so rather than print a number we can't stand behind, `/usage` links
each provider's billing dashboard, the authoritative source for spend.

## What `/usage` shows

```
session  20260530-153012.482917
  usage by model:
    claude-opus-4-7:    265 input, 103,432 output, 22,503,118 cache read, 457,012 cache write
    claude-haiku-4-5:   1,200 input, 10,712 output, 1,310,484 cache read, 117,933 cache write
  total tokens  24,504,156

rate limits  (live, from last response)
  tokens    1,824,000 / 2,000,000 remaining · resets in 41s
  requests  3,998 / 4,000 remaining · resets in 41s

today
  total tokens  213,891

account
  provider: openai (pay-per-use API key)
  billing dashboard: https://platform.openai.com/usage — the source of truth for spend
```

The block renders in an inline overlay below the cmdline (the same
surface the cheatsheet and the pickers use), not in chat scrollback —
token tallies are transient inspection, not part of the conversation,
so they never bloat the history the model re-reads. Press any key to
dismiss it. The panel is read-only and safe to invoke mid-turn — it
doesn't cancel a streaming response.

The per-model breakdown is sorted by total tokens (highest first) and
reuses the session's `ModelUsage` map. Sessions that mixed providers
or models (Claude for code review, Gemini for grep) show each model's
tokens separately.

### Live rate limits

OpenAI, Anthropic, and xAI return per-minute rate-limit headers on
**every** successful response — no admin key, no extra request. A
client middleware (`internal/adapter/ratelimit.go`) snapshots them off
each turn and `/usage` surfaces the latest as a "rate limits (live)"
block: remaining/limit token and request headroom for the current
window, with a reset countdown. The snapshot is in-memory and reflects
the most recent response, so the block only appears after the first
turn of a session and disappears on restart until the next turn.

This is the one quota signal the providers *do* return on the
inference key. The per-account **cost / spend** APIs (OpenAI's
`/v1/organization/costs`, Anthropic's `/v1/organizations/cost_report`)
need a separate **admin/org key** and report org-wide month-to-date
totals with a lag — they can't give per-session cost — so `/usage`
doesn't call them; the dashboard link covers that need.

## Why no dollar figure

Pricing the tokens we count would mean shipping a per-model rate table
and maintaining it by hand:

- **No pricing API.** OpenAI, Anthropic, Gemini, and xAI publish rates
  on web pages, not through an endpoint the inference key can read.
- **It drifts.** A bundled table is stale the moment a price changes,
  and it has no entry at all for a model released after the last update
  (and new models ship constantly).
- **The list price isn't the invoice anyway.** Promotional credits,
  committed-use and enterprise discounts, batch pricing, and
  context-length tiers all move the real number in ways the inference
  API can't see.

So a computed "≈$X" would be an unverifiable guess wearing the costume
of an exact figure. The honest surface is exact token counts (which we
have) plus a link to where the real dollars live.

## Per-provider behavior

| Provider | What `/usage` shows |
|---|---|
| `anthropic`, `openai`, `gemini`, `xai`, `openai-compatible` (OpenRouter, Groq, …) | Per-model token counts + billing-dashboard link |
| `openai-auth` (ChatGPT subscription) | Per-model token counts + plan/reset (best-effort `/backend-api/me` probe, 429-memo fallback) |
| `copilot` (GitHub Copilot subscription) | Per-model token counts; no public quota endpoint |
| `ollama` (local) | Token counts when the runtime reports them; no billing dashboard |
| `openai-compatible` → NVIDIA NIM (`integrate.api.nvidia.com`) | Token counts only — local / credit-based |

### Billing dashboards

`/usage`'s account block links each provider's public billing surface —
the authoritative answer to "what did this cost":

| Provider | Billing dashboard |
|---|---|
| Anthropic | `https://console.anthropic.com/settings/billing` |
| OpenAI API | `https://platform.openai.com/usage` |
| ChatGPT (`openai-auth`) | `https://chatgpt.com/account` |
| Copilot | `https://github.com/settings/billing/summary` |
| Gemini | `https://aistudio.google.com/app/billing` |
| xAI | `https://console.x.ai/team` |

For ChatGPT subscription accounts the `/backend-api/me` probe adds plan
+ email when the endpoint cooperates. It's undocumented and may change
without notice; we cache the result for 5 minutes per process and
silently fall back if a subsequent probe fails.

Account identity is sourced from each provider's own API, never from
config. Only `openai-auth` exposes one today (email + plan, via the
probe above); API-key providers don't surface the key holder's
name/email on the inference key, so `/usage` shows none for them.

## Where the data comes from

- Each cloud adapter parses the provider's usage field on its final
  stream event (`message_delta` for Anthropic, `response.completed`
  for the OpenAI Responses APIs, the empty-`choices` chunk for Chat
  Completions with `stream_options.include_usage: true`, and
  `usageMetadata` for Gemini).
- The neutral `adapter.Message.Usage` field carries normalized counts:
  `input_tokens`, `output_tokens`, `cache_creation_tokens`,
  `cache_read_tokens`, `reasoning_tokens`.
- `session.Session.AddUsage(model, u)` sums each turn into `TotalUsage`
  plus a per-model breakdown. Sessions persist these alongside the
  message log in `~/.yottacode/sessions/<id>.json`.
- The `/usage` daily rollup scans the sessions directory and decodes
  only the metadata + usage fields (Messages stay on disk) so the
  command stays cheap to run.
- `internal/cost/dashboards.go` maps each provider to its billing
  dashboard URL — the only piece of the former price catalog still in
  the tree.

## Backward compatibility

The `Usage` field on `adapter.Message` is a pointer with `omitempty`;
`Session.TotalUsage` uses `omitzero` and `Session.ModelUsage` uses
`omitempty`. Session files written before the usage fields landed
continue to load unchanged, and sessions that haven't recorded a turn
yet stay byte-identical to the old shape on disk.
