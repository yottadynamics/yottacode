---
name: performance-profiler
description: Find and fix performance problems with measurement, not guesses. Use when the user asks "why is X slow?", "this is taking forever", "optimize this function", or any request where speed/memory/throughput is the concern.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/alirezarezvani/claude-skills/tree/main/engineering/performance-profiler
---

# Performance profiling

The first rule: **measure, don't guess.** Most "obvious" performance fixes target the wrong thing because the obvious code path turns out not to be the bottleneck. Profiling is the discipline of letting the measurement pick the target.

The second rule: **measure before and after.** A "faster" version that you didn't compare against the baseline isn't faster — it's untested.

## 1. Define the workload

Before profiling, name what you're measuring:

- **The input**: a specific request, a specific file size, a specific dataset. Reproducible — the next agent should be able to run the same workload.
- **The metric**: wall-clock, CPU time, memory allocations, RSS, p99 latency, throughput. One primary metric; secondary metrics are noise unless explicitly tracked.
- **The environment**: machine, build flags, debug symbols, GC settings. A debug build is not a benchmark; a release build with profiler symbols is.

If the user said "it's slow", get specific before profiling: which call? With what input? What's the target?

## 2. Establish a baseline

Run the workload three times and record:

- The metric's value (median of 3).
- The variability (range of 3).
- The system load during the run (idle CPU helps; a busy box doesn't).

The baseline is the number every later run is compared against. **Write it down** — don't trust memory. A regression has to beat the *baseline*, not "what you remember from earlier".

If the baseline is too noisy to compare (range >20% of median), fix the noise first. Noise comes from: thermal throttling, other processes, debug builds, network variance. Each is fixable.

## 3. Pick the right profiler

Match the question to the tool:

| Question | Tool (language-specific) |
|---|---|
| "Which functions burn the most CPU?" | Go: `pprof -cpu` · Python: `py-spy record` / `cProfile` · Node: `--cpu-prof` / `clinic flame` · Rust: `cargo flamegraph` · JVM: async-profiler |
| "Which allocations dominate?" | Go: `pprof -alloc_objects` / `-inuse_space` · Python: `memray` / `tracemalloc` · Node: heap snapshot · Rust: `dhat` / `valgrind --tool=massif` |
| "Where is the latency?" | Go: `httptrace` + spans · OpenTelemetry tracing for any language · `perf record -g` for native |
| "What does the kernel do?" | Linux: `perf record -F 99 -ag` then `perf script | stackcollapse-perf.pl | flamegraph.pl` |
| "Where are the goroutines / threads blocked?" | Go: `pprof -block` / `-mutex` · pyrasite / py-spy dump · jstack |

Don't reach for benchmarks (`go test -bench`, `pytest-benchmark`) until you've profiled. Microbenchmarks answer a different question — they're for proving a *change* helps after profiling identified what to change.

## 4. Read the flamegraph

A flamegraph's width is time; depth is the call stack. The widest box at the bottom is where you start.

- **Wide leaf** (a function with no children, near the top) — its own body is the cost. Optimize this function.
- **Wide internal node** — the function is fast itself, but its callees are heavy. Walk up the stack to find the cluster causing the load.
- **Many narrow towers from a single root** — the root is called many times. Question whether the call itself is the problem (cache it, batch it, avoid it).

Common mistakes:

- Optimizing the **deepest** function instead of the **widest**. Deep ≠ slow.
- Reading the wrong axis. The vertical position is *which function*; the horizontal extent is *how much time*.
- Sampling too short. A 100ms run with a 99Hz sampler is 10 samples. Run long enough to get ≥1000 samples.

## 5. Form a hypothesis, then verify

Before changing code, write down:

- "I believe the cost is in `<function>` because `<observation>`."
- "If I change `<thing>`, the metric should drop by `<rough estimate>`."

Then change one thing. Re-measure. If the metric moved the predicted direction by roughly the predicted amount, the hypothesis is confirmed and you can move on. If it didn't, the hypothesis was wrong; back out the change and form a new one.

Changing five things and re-measuring once is not optimization — it's gambling.

## 6. Common wins (when the flamegraph supports them)

These are not "always do this" — they're "if the profile points here, here's the lever":

- **N+1 queries** — one big query instead of N small ones. Often the highest-impact change.
- **Unbounded allocations in a hot loop** — pre-allocate, reuse buffers, pool.
- **JSON / serialization in inner loops** — switch to a binary format, or cache the result.
- **Repeated computation** — memoize, cache, or compute outside the loop.
- **Synchronous I/O in a request path** — make it async, or move it to a worker.
- **String concatenation in a loop** (interpreted languages) — use a builder / join.
- **Lock contention** — fewer locks, finer-grained locks, or lock-free where applicable.
- **GC pressure** — fewer short-lived allocations; longer-lived objects so they survive minor GC.

Each of these is the right move *when the profile says so*, and the wrong move otherwise.

## 7. Verify the win

After the change:

1. Re-run the workload three times.
2. Compare median to baseline.
3. State the result as a percentage: "p99 latency went from 240ms to 87ms, a 64% reduction."
4. Confirm the change didn't regress correctness — run the test suite.

If the change made things *worse*, back it out. "Worse" is not subjective: the median moved the wrong direction relative to the baseline.

## 8. Document and stop

A performance fix that isn't documented gets undone by the next reader who thinks the code is "weird". Leave:

- A comment in the code explaining the why, not the what.
- The before/after numbers in the commit message.
- A note (TEST/benchmark file or doc) explaining how to re-run the workload.

Then stop. The temptation to "while I'm here, also speed up X" is how optimization PRs grow unreviewable. Ship the one win; queue the rest.

## 9. Anti-patterns

- **Guess-driven optimization** — "this looks slow, let me rewrite it" — usually targets the wrong place.
- **Micro-optimization in cold paths** — saving 10ns in a function called once per request is invisible.
- **Optimizing for the benchmark, not the workload** — the benchmark trains a behavior; the real load behaves differently.
- **Switching algorithm without measuring data** — O(n log n) sort is faster than O(n²) sort only for n large enough that the constants don't dominate.
- **Skipping correctness verification** — a fast wrong answer is the worst possible outcome.
