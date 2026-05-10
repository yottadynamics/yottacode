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
```

In the TUI:

```text
/sessions
/sessions <id-or-name>
```

Runtime flags like `--max-iterations` and `--bypass-permissions` are not stored in the session. Pass them again when resuming if you need them.

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
