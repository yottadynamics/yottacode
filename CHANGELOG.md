### Fixed

- **Edit tools fail less and recover faster.** From a session where 32 tool
  calls errored (16 of 48 `edit_file`, 9 of 19 `edit_anchored`, 4 of 4
  `apply_hashline`, 2 of 2 `apply_diff`):
  - `apply_hashline` no longer rejects an `anchor` hunk because the model also
    sent default-filled `"hash":""`, `"offset":0`, `"length":0`, or copied the
    anchor's own hash into `hash`; those retries always failed identically. A
    relocated anchor is reported in the result, as for `edit_anchored`.
  - `edit_anchored` / `apply_hashline` relocate a stale anchor whose line moved
    (within 100 lines, exactly one match; `edit_anchored` reports the move in
    its result), and otherwise quote the current
    lines with fresh anchors in the error instead of only a new hash.
  - `apply_diff` rejects apply_patch-format and header-less patches before the
    approval prompt (previously you approved a patch that failed immediately)
    and the error includes a unified-diff example.
  - `edit_file` errors now list the lines of an ambiguous match, say when
    `new_string` is already present (a retry after success), show the actual
    text near the closest line, and name the cwd on file-not-found; `read_file`
    gets the same cwd hint.
  - The system prompt tells the model to re-read after an edit and not to
    resubmit a failed edit unchanged.
- **Edit tools preserve file integrity under real session churn.** `edit_file` now uses compare-and-swap atomic writes, `edit_anchored` preserves CRLF/mixed line endings and unterminated files, `apply_diff` bounds `git apply` and stops retrying after cancellation or timeout, and `grep` reports its resolved root and output truncation instead of silently returning incomplete evidence.
- **ACP: clients can now select the model.** `yottacode acp` advertises a
  `model` session config option (category `model`) listing every model from
