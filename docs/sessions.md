# Sessions

Sessions preserve your conversation history, tool calls, and tool results across restarts.

## How sessions are saved

Every completed turn is saved atomically to:

```text
~/.yottacode/sessions/<id>.json
```

Session ids are timestamp-like strings, for example:

```text
20260426-150100.123456
```

There is no manual save step.

Each user message in a session also gets a per-prompt checkpoint capturing the conversation and any files the agent is about to touch. Open the picker mid-session via `/checkpoints` or **Esc Esc** to restore conversation, files, or both. See [tui-slash-commands.md](tui-slash-commands.md#checkpoints---checkpoints--esc-esc).

Token usage (input, output, and any prompt-cache hits) is recorded per turn alongside the assistant message and accumulated into per-model totals on the session. See [`/usage`](cost.md) for how those totals surface.

## List sessions

```bash
yottacode sessions list
yottacode sessions list --json
```

In the TUI:

```text
/sessions
```

## Resume a session

```bash
yottacode sessions resume <id-or-name>
yottacode --resume <id-or-name>
yottacode --continue                  # the most recent session in this directory
yottacode -c                          # short form
```

`--continue` (mirrors Claude Code's flag) skips the picker and resumes the newest saved session whose cwd matches the current directory — useful for "I quit a few minutes ago and want to pick up where I left off." When no saved session matches the current directory, `--continue` errors out with a hint. `--continue` and `--resume` are mutually exclusive; pass one or the other.

In the TUI:

```text
/sessions
/sessions <id-or-name>
```

Runtime flags like `--max-iterations` and `--yolo` are not stored in the session. Pass them again when resuming if you need them. Mode state (auto / plan) is also not persisted: a session that ended in auto mode resumes in normal mode, same as Claude Code — use `Shift+Tab` to re-enter the mode you want.

## Summarized resume

Large sessions can consume a lot of context before the first new prompt. Use summarized resume to carry forward the important parts compactly:

```bash
yottacode sessions resume <id-or-name> --summarized
```

In the TUI sessions picker, press `s` to toggle summarized loading.

## Rename a session

```bash
yottacode sessions rename <id-or-name> my-feature-work
```

Names are convenience labels. The canonical identity is still the session id.

## Export a session

```bash
yottacode sessions export <id-or-name>
yottacode sessions export <id-or-name> path.md
yottacode sessions export <id-or-name> path.md --force
```

Export writes a readable Markdown transcript with turns, tool calls, and tool output. System messages are omitted.

## Search sessions with recall

```text
/recall authentication
```

Recall uses a local SQLite FTS5 index at:

```text
~/.yottacode/index.sqlite
```

The index is rebuilt/backfilled automatically from saved session files.

## Automatic recall of prior conversations

Beyond the manual `/recall` command and the agent's `session_recall` tool, yottacode can bring relevant past conversations back **on its own**, so the agent picks up where you left off without being asked.

At the start of each turn it embeds your message, semantically searches your earlier sessions, and injects the most relevant excerpts into the system prompt as a short background block. Only conversations that clear a similarity threshold are injected — when nothing is relevant, nothing is added.

This is the episodic counterpart to memory retrieval, and it is **reads-only**: it never writes memory. It requires a local embedding model (Ollama, same one used for semantic memory retrieval); when that is unavailable it silently falls back to the manual `session_recall` tool. Session embeddings are stored alongside the FTS5 index in `~/.yottacode/index.sqlite` (a `message_vectors` table), backfilled in the background at startup and incrementally after each turn — so a conversation becomes recallable in later sessions without restarting.

When a turn pulls in prior conversations, the thinking-row footer shows `recalled N conversations`, so the injection is visible rather than silent. To tune `min_score` against real usage, set `YOTTACODE_RECALL_DEBUG=1` and each firing is logged with its cosine scores to `~/.yottacode/recall-debug.log`.

Configure it under `[retrieval.session_recall]` in `config.toml`:

```toml
[retrieval.session_recall]
auto = true          # per-turn injection on/off (manual session_recall stays available either way)
scope = "project"    # "project" = only sessions in the current directory (never mixes projects); "user"/"all" = whole store
top_k = 3            # max excerpts injected per turn
min_score = 0.6      # cosine-similarity floor (0.0–1.0), calibrated for nomic-embed-text — only on-topic history surfaces
max_bytes = 2000     # size cap on the injected block (0 = no byte bound)
```

`scope = "project"` is the default and never surfaces another project's conversations. To turn the behavior off entirely, set `auto = false`.

## Clear vs delete

`/clear` saves the current session, then starts a new session with the same system prompt and memory. It does not delete anything.

To delete a session, remove the JSON file:

```bash
rm ~/.yottacode/sessions/<id>.json
```

## Summarization snapshots

`/summarize` writes a pre-compression snapshot before rewriting in-memory history:

```text
~/.yottacode/sessions/<id>-pre-summary-<timestamp>.json
```

Keep these files if you may need to inspect or restore pre-summary history.
