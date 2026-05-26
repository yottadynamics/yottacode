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

## Connection refused or red status dot

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

Default max iterations is `25`. Increase it for larger tasks:

```bash
yottacode --max-iterations 50
```

Keep the limit finite; it protects you from runaway tool loops.

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

The TUI uses inline rendering so your terminal owns scrollback. If rendering becomes messy after a resize, clear the terminal or restart yottacode; the saved session can be resumed.

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
- exported session Markdown if relevant: `yottacode sessions export <id-or-name>`
