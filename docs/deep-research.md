# Deep research

`/deep-research <question>` answers a question that needs sourced claims
(comparisons, investigations, "research X") and leaves a cited markdown
report in the current directory. It is slow and token-heavy by design; use
it instead of a quick web lookup only when the answer has to be checked.

```
/deep-research Bootc and chunking for patching
/deep-research --breadth 2 Compare bootc and ostree-native containers
```

`--breadth N` (2–6, default 4) is the most questions the planner may create,
which is also the number of researchers running in parallel. It is the main
cost lever: the research phase scales with it, so `--breadth 2` costs roughly
half of the default. The flag is recognized only before the question;
`--breadth 9` is an error, not a clamp.

The chat shows a short cited summary; the full report is written to
`deep-research-<slug>.md` in the working directory. An existing report is
never overwritten (`-2`, `-3`, … suffixes), and the write needs no approval
prompt. The command is blocked in plan mode because it writes a file.

## How it works

The orchestration is the `deep_research` tool (`internal/agent/deep_research*.go`),
deterministic Go driving read-only subagents. The model does not decide how
many agents run or what counts as verified. The phases follow the
deep-research workflow in Grok's CLI.

| Phase | Agent | What it does |
| --- | --- | --- |
| Plan | `research-planner` | Splits the query into ≤ `breadth` independent questions (default 4, range 2–6). A failed planner falls back to researching the original query as one question. |
| Research | `researcher` × N, parallel | One question each. Returns ≤ 6 atomic claims, each with evidence, source title, URL/path, source type and confidence, plus uncertainties. |
| Verify | `research-verifier` × ≤ 2, parallel | Claims (capped at 24) are sharded round-robin. Each verifier re-opens the cited sources and returns exactly one verdict per claim ID. |
| Report | `research-synthesizer` | Writes the body from verified claims only. The caller appends `## Sources` and `## Coverage and uncertainty`. |

## What is validated in code

- **JSON shape.** Each shape is defined once in Go
  (`deep_research_schema.go`) and rendered into every prompt as a JSON Schema;
  a test checks the examples in the builtin agent definitions against it.
  There is no provider-level schema enforcement for subagents, so this is
  instruction plus validation: a reply that does not decode, or fails its
  checks, is retried once with the rejection reason spelled out, and a second
  failure counts as that agent failing.
- **Field sizes.** Claim, evidence, title and locator text from sub-agents is
  clipped, so one runaway reply cannot bloat the verifier packet or the report.
- **Claims.** A claim needs non-empty claim, evidence, source title and source
  locator, or it is dropped (and counted in the coverage notes).
- **Verdicts.** A verifier's reply must contain exactly one verdict for each of
  its claim IDs. A missing, duplicate or unknown ID discards the whole shard.
  A claim is kept only when the verdict is `supported` and carries independent
  evidence, source title and source locator.
- **Citations.** The synthesized body must use every `[Sn]` marker, invent
  none, and contain no `Sources`/`References` section of its own. Otherwise the
  report falls back to a plain finding list.
- **Prompt injection.** The query, every claim and every fetched page are
  passed as JSON-encoded data and each agent is told they are not instructions.

## Partial reports

Anything that degrades the run (a failed question, a lost verifier shard, a
claim that failed verification, a rejected synthesis) marks the report
`Status: Partial` and lists why under *Coverage and uncertainty*. The
researchers' own stated uncertainties ("that page returned 403", "no
benchmarks found") are listed there too but do **not** make a run Partial: they
are scope notes on an otherwise verified answer, and counting them would label
nearly every run Partial. A run with no verified claims still writes a report
saying so; it never answers from unverified material.

## Limits and cost

- **Cost line.** Every result and saved report ends with what the run spent,
  e.g. `Cost: 8 agent runs (planner 1, researcher 4, verifier 2, synthesizer 1),
  at most 4 at once · ~1.6M tokens (researcher 1.1M, verifier 460K, synthesizer
  26K, planner 2K) · 3m30s`. The runs are counted over the whole workflow
  (planner, then researchers, then verifiers, then synthesizer), so the total is
  larger than what the dock shows at any moment; "at most N at once" is the peak
  you could have seen there. `--breadth` sets only the researcher count: the
  planner, the 2 verifiers and the synthesizer are fixed, so a `--breadth 2` run
  is always 6 runs. The split by agent type shows which phase spent the tokens; types
  that recorded none are left out. Cost varies far more with the question than
  with `--breadth`: one question that needed many long pages cost about five
  times another at the same breadth. Retries count as agent runs. Tokens are
  the exact provider-reported usage the subagent registry records for this
  run's agents. Where there is no registry the token part is omitted, not
  estimated.
- **One run at a time.** Starting a second run while one is active is refused
  (`a deep-research run is already in progress`); stop it from `/subagents` or
  wait. Concurrent runs would starve each other's researchers against the
  subagent cap and blur the token attribution above.
- **Least privilege.** The researchers and verifiers read arbitrary web pages
  and can reach the network, so they get `web_search` and `fetch_url` and
  nothing else: no `read_file`, `grep`, `glob`, document or memory tools. A
  hostile page therefore cannot steer an unattended agent into reading a local
  secret and leaking it through a claim or a fetch URL. The cost is that
  deep research cannot inspect your workspace; ask the normal agent for that.
- **Untrusted text.** Everything the research agents return comes from web
  pages, so control characters (terminal escape sequences, bell, NUL) are
  stripped from claims, evidence, titles, locators, verifier notes and the
  report body before they reach the saved file or your terminal. Newlines and
  tabs are kept.
- **Iteration budget.** `researcher` and `research-verifier` run with
  `max_iterations: 30` instead of the session's 100. An agent that hits it
  fails that question or shard and is **not** retried (a retry would spend a
  second full budget to hit the same wall), so a wandering researcher costs at
  most one budget.
- At most `breadth` researchers (≤ 6) and 2 verifiers run at once, further
  bounded by the session's foreground-subagent cap (`[subagents]` max
  concurrent); extra agents queue.
- Each researcher/verifier is a normal subagent: it counts against the
  session subagent token budget and the per-child iteration cap, and its
  transcript is viewable in `/subagents`.
- `deep_research` is stripped from every subagent's tool set, so a child can't
  start its own fan-out.

## Running in the background

In the interactive TUI the whole workflow is one background task
(`deep-research`, `notify_on_done`). The command returns immediately, you keep
the prompt, and:

- the dock gets a **Workflows** section above the subagents rows, with a card:

  ```
  Workflows · 1
    ◆ Workflow deep-research — Research · 4 agents running · 1 done · 58s   deep-research-03016201
        Plan ✓ · Research ● · Verify ○ · Report ○ — 4 question(s), up to 4 at a time
        “chunkah vs build-chunked-oci”

  subagents · 4 running  tab to inspect
    ▸ researcher  fetch_url(https://…)   …
  ```

  The title shows the current phase, how many agents are running now (the rows
  beneath it) and how many have finished, and elapsed time. The card is selectable like any row (`tab`, `↑/↓`, `enter` opens
  its log); on a narrow terminal the trailing task id is dropped first;
- `/subagents` opens its log (phase lines, then the summary) and `s` stops it;
  stopping cancels every agent it started and writes no report;
- when it finishes you get the usual background-completion banner, and the
  summary (with the cost line and the saved file's path) is printed right
  below it. Esc on an unrelated turn does not cancel the run.

### No model turn at either end

In the TUI the model is not involved in starting or delivering a run:

- `/deep-research` starts the tool itself and records the exchange in the
  conversation (your command, then a "started in the background" line). It also
  creates a `/checkpoints` entry, like any turn, so you can rewind the
  conversation to before the command. That restores the conversation only: the
  run is detached and the report file it writes is not tracked;
- when the run finishes the TUI prints the summary itself and records it in
  history as an assistant message, so follow-up questions ("which should I use?")
  see the report.

The workflow is deterministic Go and its summary is already verified and
citation-checked, so a model hop would only add cost (a full re-read of the
conversation, ~50–100K tokens per turn) and room for uncited commentary. It
also means the cost line below is the **whole** cost of the command in the TUI.
A completion that lands while a turn is running waits for the turn to end, since
the agent loop owns the history until then.

The model-driven path is still used where the TUI can't start the run directly:
in plan mode (the tool stays blocked), and in `yottacode run` and ACP, which
have no dock to host a detached task — there the tool blocks inside the turn and
each phase change is a stderr / diagnostics line. If you ask for deep research in
plain words and the model calls the tool itself, the run is still a background
task and its result is still delivered by the TUI, not relayed by the model.

The chat summary is the opening answer only (≤ 1500 characters) plus the file
path; every finding, the sources and the coverage notes are in the saved file.

## Not in v1

- Browser-assisted fetching (`browser_*` tools prompt for approval on
  non-loopback pages, which doesn't suit unattended parallel agents).
- A `fetch_url` size cap specific to research agents. `fetch_url` already
  caps each page at 64 KB (256 KB at most); revisit if the cost line shows page
  content dominating.
