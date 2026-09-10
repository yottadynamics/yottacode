# `yottacode run` recipes

`yottacode run` executes one prompt non-interactively with the same tools, memory,
provider configuration, and permissions as the TUI.

- Default text mode preserves the existing streamed assistant-content stdout
  contract. Reasoning, tool progress, and status lines go to stderr.
- `--format json` writes one JSON object to stdout after the turn. It still uses
  the normal process exit status, so capture the status before parsing failures.
- Mutating tools still require approval. Do not add `--yolo` just to make a CI
  example pass. Use it only on a trusted disposable branch or worktree with
  reviewed deny rules.

JSON results have this shape (zero-valued usage counters may be absent):

```json
{
  "content": "...",
  "tool_calls": [{"name": "read_file", "summary": "read_file(README.md)"}],
  "usage": {
    "input_tokens": 1240,
    "output_tokens": 380,
    "cache_read_tokens": 900
  },
  "exit_reason": "ok",
  "error": null,
  "session_id": "..."
}
```

`exit_reason` is `ok`, `error`, or `iter_cap`. An `error` result also returns a
non-zero shell status. Reaching the iteration cap keeps the existing shell-exit
behavior and is reported separately as `iter_cap`.

## 1. PR description generator

This artifact is intended for a person to read or paste into a pull request, so
plain text is the useful output.

```bash
yottacode run \
  "Read the current branch diff and commit history. Draft a concise pull request description with Summary, Testing, and Risks sections. Do not modify files." \
  > /tmp/pr-description.md

cat /tmp/pr-description.md
```

Expected stdout shape:

```markdown
## Summary
- ...

## Testing
- ...

## Risks
- ...
```

## 2. Automated codemod

Run codemods only in a disposable worktree or branch. `--yolo` is required for
unattended writes and remains subject to explicit deny rules.

```bash
set +e
yottacode run --format json --yolo \
  "Rename the deprecated Widget.LegacyID field to ExternalID across this repository. Update comments and tests, run the focused test suite, and summarize the files changed." \
  > /tmp/codemod-result.json
run_status=$?
set -e

jq -e '.exit_reason == "ok" and (.tool_calls | length > 0)' \
  /tmp/codemod-result.json
jq -r '.content' /tmp/codemod-result.json
exit "$run_status"
```

Expected stdout is one JSON object. Successful codemods have
`"exit_reason":"ok"`, an `error` of `null`, and tool summaries that normally
include edit and test tools. Review `git diff` before keeping the branch.

## 3. Test-failure triager

This recipe consumes structured fields in CI while retaining the shell status.

```bash
set +e
yottacode run --format json \
  "Read test-output.txt and the relevant source without modifying files. Identify the root cause, likely owning files, and the smallest safe fix. Return a concise triage report." \
  > /tmp/test-triage.json
run_status=$?
set -e

jq -e '.content | type == "string"' /tmp/test-triage.json
jq -r '.content' /tmp/test-triage.json

if [ "$run_status" -ne 0 ]; then
  jq -r '.error' /tmp/test-triage.json >&2
  exit "$run_status"
fi
```

Expected stdout is one JSON object. A provider or turn failure still produces
valid JSON with `exit_reason` set to `error` and `error` set to a message.

## 4. Dependency-audit summarizer

Generate the machine audit first, then ask yottacode to turn it into a report for
a human reading a CI log or artifact.

```bash
set +e
govulncheck -json ./... > /tmp/govulncheck.json
audit_status=$?

yottacode run \
  "Read /tmp/govulncheck.json. Summarize reachable vulnerabilities, affected packages, available fixes, and recommended priority. Do not modify files." \
  | tee /tmp/dependency-audit.md
summary_status=${PIPESTATUS[0]}
set -e

if [ "$summary_status" -ne 0 ]; then
  exit "$summary_status"
fi
exit "$audit_status"
```

Expected stdout is a Markdown summary. A yottacode failure takes precedence;
otherwise the final exit preserves the dependency scanner's status rather than
treating a successful summary as a clean audit. This recipe uses Bash because
`PIPESTATUS` is not portable POSIX shell syntax.

## 5. Changelog drafter

Use JSON when another script extracts the draft into a repository file.

```bash
set +e
yottacode run --format json \
  "Read the current git diff and recent commit subjects. Draft one Keep a Changelog bullet for the Unreleased section. Do not modify files and return only the bullet." \
  > /tmp/changelog-draft.json
run_status=$?
set -e

if [ "$run_status" -eq 0 ]; then
  jq -r '.content' /tmp/changelog-draft.json > /tmp/changelog-bullet.md
  cat /tmp/changelog-bullet.md
else
  jq -r '.error' /tmp/changelog-draft.json >&2
fi

exit "$run_status"
```

Expected stdout is one JSON object; `/tmp/changelog-bullet.md` contains only the
assistant's proposed Markdown bullet. Review it before editing `CHANGELOG.md`.

## Legacy status receipt

`yottacode run --json` remains available for compatibility. It keeps the final
answer on stdout and appends the older status receipt to stderr. It cannot be
combined with `--format json`; migrate new automation to `--format json`.
