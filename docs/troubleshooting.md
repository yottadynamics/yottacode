# Troubleshooting

## Missing model or base URL

If yottacode exits with a configuration error, set both values:

```bash
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.openai.com/v1
```

For remote providers, also set an API key:

```bash
export YOTTACODE_API_KEY=sk-...
```

Or run:

```bash
yottacode setup
```

## Connection refused or provider errors

Check that the provider is running and the base URL is correct:

```bash
curl "$YOTTACODE_BASE_URL/models"
yottacode doctor
```

For Ollama:

```bash
ollama serve
ollama pull qwen3.5:latest
```

Use `http://localhost:11434/v1` as the base URL.

## Model not visible

Run:

```bash
yottacode doctor
yottacode model fetch
```

Common causes:

- wrong provider account or API key
- model id typo
- provider does not expose the model on `/models`
- using a cloud model name against a local/custom endpoint

## Tool call loop hits max iterations

Default max iterations is `100`. Raise it for unusually large tasks:

```bash
yottacode --max-iterations 300
```

Keep the limit finite; it protects you from runaway tool loops.

## `cannot unmarshal string into Go struct field … of type int`

Seen as a repeating tool error (e.g. `web_search: invalid args: json: cannot
unmarshal string into Go struct field .max_results of type int`) with the model
retrying the same call. Some open models — notably Meta Llama 3.1/3.3 instruct
on NVIDIA NIM, Ollama, and vLLM — emit numeric/boolean tool arguments as JSON
strings (`"5"` instead of `5`).

yottacode now normalizes these against each tool's schema automatically, so
**upgrading resolves it** with no configuration. If you still see it, the model
is likely emitting the tool call as plain text rather than a structured call —
a separate model limitation that argument normalization cannot fix; switch to a
model that supports structured tool calling (e.g. NVIDIA's Nemotron, Mistral,
Qwen, or DeepSeek on the same endpoint).

## `adapter: empty completion` on NVIDIA NIM

Some NVIDIA NIM models have returned an empty streaming response when optional
streaming usage reporting is requested. yottacode omits that optional usage
probe for NVIDIA NIM and other local/free OpenAI-compatible endpoints, so
**upgrading resolves it**. If the error persists, run `yottacode doctor` to
verify the model id and endpoint, then try a fresh session to rule out a stale
provider-side stream or over-large context.

## `unexpected end of JSON input`, or a provider 400 that repeats every turn

Two linked symptoms from one cause — a tool call that arrived with empty or
truncated JSON arguments:

- `write_file: invalid args: unexpected end of JSON input` (a tool ran against
  an empty argument string), or
- a strict provider (notably NVIDIA NIM) returning `400 Bad Request` with
  `Expecting ',' delimiter …` on **every** turn — including unrelated prompts,
  and even after switching models — because the malformed call was replayed
  from conversation history into each request body.

yottacode now normalizes an empty arguments payload to `{}`, rejects a genuinely
truncated call before it runs or enters history, and re-sanitizes replayed
history when building each request, so a bad call can no longer wedge the
session. **Upgrading resolves it.** If a session started on an older build is
still stuck, begin a new session to drop the already-poisoned history.

## Sandboxed tests cannot resolve DNS or execute Go test binaries

If sandboxed `run_tests` fails with `lookup proxy.golang.org on [::1]:53` (or a
similar loopback DNS refusal), upgrade yottacode so new sandbox sessions get the
default runtime DNS resolvers. If your network requires internal resolvers,
override them in config instead of baking `/etc/resolv.conf` into an image:

```toml
[sandbox]
backend = "podman"
dns = ["10.0.0.53"] # example VPN/corporate resolver IP
```

If sandboxed Go tests fail with `fork/exec /tmp/go-build.../*.test: permission
denied`, upgrade yottacode. Sandboxed `run_tests` now keeps `/tmp` mounted
`noexec` for hardening but exports `TMPDIR`, `GOTMPDIR`, `GOCACHE`, and
`GOMODCACHE` under `/var/tmp/yottacode-go/<workspace>/` inside the sandbox so Go
can compile and execute test binaries safely without leaving repo-root `.cache/`,
`.config/`, `.yottacode/tmp/`, `.scratch/`, or `go/` directories that would
break later `go test ./...` discovery or workspace-root tests.

## The trust prompt fires on every launch

The first-launch trust prompt records cwd in `~/.yottacode/trusted-roots.json` on Yes. If you see it again on a directory you already accepted, the cwd is most likely a fresh path (different absolute path, different worktree, different bind-mount). List and add directly:

```bash
yottacode trust list
yottacode trust add /path/to/repo
```

Trust covers every subfolder of an added root. To bypass the prompt in CI runs, set `YOTTACODE_TRUST_ALL=1` (session-only — does not persist).

## Approval prompts are too frequent

Use project-local allow rules for operations you trust:

```json
{
  "permissions": {
    "allow": ["Bash(go test *)", "Edit(docs/**)"]
  }
}
```

Save personal rules in `.yottacode/permissions.local.json` and gitignore that file.

## A saved memory is wrong or stale

Inspect and prune agent-managed memories:

```bash
yottacode memory list --scope user
yottacode memory list --scope project
yottacode memory forget --scope <user|project> <name>
```

Or browse them in the TUI: `/memory` → **Browse user memories** / **Browse project memories**, then `d` on the offending entry. Restart yottacode or reload memory from the TUI so the active session picks up the change.

## The agent keeps making the same mistake

Put durable corrections in curated memory:

- user-wide preference: `~/.yottacode/USER.md`
- repo-specific convention: `./.yottacode/YOTTACODE.md`

Open the memory picker:

```text
/memory
```

## Large session or context warnings

Use:

```text
/summarize
```

or start fresh with:

```text
/clear
```

Summarization snapshots are saved before history is compressed.

## Pasted text is collapsed in the input

Large pastes are shown as a short marker to keep the input line usable. The full pasted text is still sent when you submit.

## Terminal rendering looks odd after resize

The TUI runs full-screen (alt-screen) and re-renders the whole frame — including its own scrollable conversation transcript — on every resize, so this shouldn't come up in normal use. If your terminal emulator itself mishandles the resize (rare), restarting yottacode always recovers cleanly; the saved session can be resumed.

## Stale edit or patch target

If an `edit_file` old string or `apply_diff` hunk no longer matches the file, the tool result is recoverable retry guidance, not a session failure. The TUI shows it as a compact stale-target hint; the agent should re-read the current file text and retry with fresh context.

## ChatGPT OAuth: callback port already in use

`openai-auth` uses a fixed loopback callback port required by the OAuth redirect allow-list. If sign-in says the callback port is already in use, another sign-in is still holding it — often an abandoned browser flow in this or another yottacode instance.

Retrying inline now closes any pending sign-in in the current TUI before starting a new one. If the holder is a different process or user, free that process and retry; on Unix-like systems, `sudo lsof -i :1455` shows the owner when permissions allow it.

## ChatGPT OAuth: `Missing required parameter: 'input[n].output'`

This is not usually a login failure. It means the `openai-auth` backend rejected the conversation history because one prior tool-result item was missing the required `output` field. Common causes are a saved/resumed session with an interrupted tool call, or an adapter history-conversion bug exposed by the stricter Responses/Codex payload validator.

Try, in order:

```text
/redo
```

If that does not help, start a fresh session:

```text
/clear
```

If you need the context from a large session, manually summarize the important state into the new prompt instead of replaying the full tool history. Re-running `yottacode openai-auth login` will not fix malformed session history.

## `--worktree` exits with "workspace trust not accepted"

`yottacode --worktree <name>` requires the repo root to be trusted.
Run yottacode once in the repo without `--worktree`, accept the
trust prompt, then retry:

```bash
cd /path/to/repo
yottacode               # accept the trust prompt
# (Ctrl-C to exit when the TUI opens, or finish your session)
yottacode --worktree feature-x
```

Trust is persistent — you only do this once per repo. See
[security-and-allow-lists.md](security-and-allow-lists.md).

## Worktree from `yottacode run --worktree` wasn't cleaned up

This is intentional. Oneshot / non-interactive runs cannot show a
keep-or-remove prompt, so they leave the worktree in place. Remove
it manually:

```bash
yottacode worktree remove <name>             # if clean
yottacode worktree remove <name> --force     # if dirty (discards uncommitted work)
```

`yottacode worktree list` shows everything under
`~/.yottacode/worktrees/<repo-slug>/`.

## Reporting bugs

Include:

- `yottacode version`
- provider and model
- `yottacode doctor` output, redacted
- a minimal reproducer
- exported session Markdown or JSONL if relevant: `yottacode sessions export <id-or-name>`
