# Cost transparency (`/usage`)

`/usage` shows the token usage and estimated cost for the current
session, with a rolling total across every session created today.
The figure is always prefixed with `~` to flag it as an estimate —
real invoices drift once batch discounts, prompt-caching tiers, and
zone-specific rates apply.

## What `/usage` shows

```
session  20260528-153012.482917
  usage by model:
    claude-opus-4-7:    265 input, 103,432 output, 22,503,118 cache read, 457,012 cache write  ($16.7102)
    claude-haiku-4-5:   1,200 input, 10,712 output, 1,310,484 cache read, 117,933 cache write  ($0.3311)
  total cost  ~$17.0413   (prices catalog 2026-05-28)

rate limits  (live, from last response)
  tokens    1,824,000 / 2,000,000 remaining · resets in 41s
  requests  3,998 / 4,000 remaining · resets in 41s

today (4 sessions)
  total tokens  213,891
  total cost    ~$18.21

account
  openai-auth (chatgpt Plus plan) — resets in 4d 7h
  signed in as: alice@example.com
  no per-request cost (subscription)
  billing dashboard: https://chatgpt.com/account
  catalog snapshot: 2026-05-28 — verify against the dashboard for exact figures
```

The block renders in an inline overlay below the cmdline (the same
surface the cheatsheet and the pickers use), not in the chat
scrollback — token tallies are transient inspection, not part of the
conversation, so they never bloat the history the model re-reads.
Press any key to dismiss it. The panel is read-only and safe to
invoke mid-turn — it doesn't cancel a streaming response.

The per-model breakdown is sorted by total tokens (highest spender
first) and reuses the session's `ModelUsage` map. Sessions that
mixed providers (e.g. Claude for code review, Gemini for grep) show
each model's tokens and cost separately.

### Live rate limits

OpenAI, Anthropic, and xAI return per-minute rate-limit headers on
**every** successful response — no admin key, no extra request. A
client middleware (`internal/adapter/ratelimit.go`) snapshots them off
each turn and `/usage` surfaces the latest as a "rate limits (live)"
block: remaining/limit token and request headroom for the current
window, with a reset countdown. The snapshot is in-memory and reflects
the most recent response, so the block only appears after the first
turn of a session and disappears on restart until the next turn.

This is distinct from cost: the providers don't return a dollar figure
in the response, so cost is always the local catalog estimate. It's
also distinct from the openai-auth 429 memo, which is populated only
*after* a rate-limit error — the live block is proactive.

Note: the per-account **cost / spend** APIs (OpenAI's
`/v1/organization/costs`, Anthropic's `/v1/organizations/cost_report`)
require a separate **admin/org key** (`sk-admin-…` / `sk-ant-admin-…`),
not the inference key yottacode authenticates with, and report org-wide
month-to-date totals with a ~5-minute lag — they can't give per-session
cost. Surfacing them would require an opt-in admin credential and is
intentionally out of scope here.

## Per-provider behavior

| Provider | What `/usage` shows |
|---|---|
| `anthropic`, `openai`, `gemini`, `xai`, `openai-compatible` (e.g. OpenRouter, Groq) | Per-model tokens + estimated cost from the local catalog. Billing dashboard URL in the footer |
| `openai-auth` (ChatGPT subscription) | Per-model token tallies + "subscription — no per-request cost". `/usage` also fires a best-effort GET against `chatgpt.com/backend-api/me` to surface the plan and signed-in email proactively; if that endpoint changes shape, the panel degrades to the 429 memo (plan + reset window captured the last time a 429 was observed) and finally to a bare "subscription" label |
| `copilot` (GitHub Copilot subscription) | Per-model token tallies + "subscription — no per-request cost"; no public quota endpoint |
| `ollama` (local) | Excluded from cost computation. The session still records tokens when the runtime reports them, but no dollar figure is shown |
| `openai-compatible` pointed at NVIDIA NIM (`integrate.api.nvidia.com`) | Excluded — same reason as Ollama. Inception credits make per-call cost undefined for end users |

### Account status

For pay-per-use providers, account-wide spend, credit balance, and
billing-period usage are gated behind admin-tier credentials that a
regular API key can't reach (Anthropic's Admin API, OpenAI's
Organization API, GCP's Cloud Billing API). Rather than pretend we
can query them, `/usage`'s footer points at each provider's public
billing dashboard:

| Provider | Billing dashboard |
|---|---|
| Anthropic | `https://console.anthropic.com/settings/billing` |
| OpenAI API | `https://platform.openai.com/usage` |
| ChatGPT (`openai-auth`) | `https://chatgpt.com/account` |
| Copilot | `https://github.com/settings/billing/summary` |
| Gemini | `https://aistudio.google.com/app/billing` |
| xAI | `https://console.x.ai/team` |

For ChatGPT subscription accounts the `/backend-api/me` probe adds
plan + email when the endpoint cooperates. The endpoint is not
documented by OpenAI and may change without notice; we cache the
result for 5 minutes per process and silently fall back if a
subsequent probe fails.

## Where the data comes from

- Each cloud adapter parses the provider's usage field on its final
  stream event (`message_delta` for Anthropic, `response.completed`
  for the OpenAI Responses APIs, the empty-`choices` chunk for Chat
  Completions with `stream_options.include_usage: true`, and
  `usageMetadata` for Gemini).
- The neutral `adapter.Message.Usage` field carries normalized
  counts: `input_tokens`, `output_tokens`, `cache_creation_tokens`,
  `cache_read_tokens`, `reasoning_tokens`.
- `session.Session.AddUsage(model, u)` sums each turn into
  `TotalUsage` plus a per-model breakdown. Sessions persist these
  alongside the message log in `~/.yottacode/sessions/<id>.json`.
- The `/usage` daily rollup scans the sessions directory and decodes
  only the metadata + usage fields (Messages stay on disk) so the
  command stays cheap to run.

## Price catalog

Per-provider price tables live in `internal/cost/` (one file per
provider family). Each entry records USD per 1M tokens for:

- `Input` (uncached prompt tokens)
- `Output` (assistant tokens)
- `CacheWrite` and `CacheRead` (for providers with prompt caching)
- `Reasoning` (when billed at a different rate than Output; 0 means
  "billed as Output")

The `CatalogVersion` constant in `internal/cost/version.go` is the
date the catalog was last verified against each provider's public
pricing page. Bump it manually when prices change — `/usage` surfaces
the date as a freshness signal in its footer.

Unknown models (anything not in the catalog) return "no price data
— tokens only" rather than guessing. To add a model:

1. Open the matching `internal/cost/<provider>.go`.
2. Add an entry to the map keyed on the model ID exactly as the API
   accepts it (aliases like `claude-sonnet-4-5` and the dated form
   each get their own row).
3. Bump `CatalogVersion`.

## Backward compatibility

The `Usage` field on `adapter.Message` is a pointer with
`omitempty`; `Session.TotalUsage` uses `omitzero` and
`Session.ModelUsage` uses `omitempty`. Session files written before
the usage fields landed continue to load unchanged, and sessions
that haven't recorded a turn yet stay byte-identical to the old
shape on disk.
