# Go debugging

yottacode exposes a small Go debugger tool surface backed by Delve's DAP server.
The intent is failed test → inspect state → patch, not a full interactive IDE
replacement.

## Requirements

Install Delve yourself and make `dlv` available on `PATH`:

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

yottacode never downloads debugger binaries. If `dlv` is missing,
`debug_start` returns an install hint and stops before executing anything.

## Tool flow

Start one debug session, set breakpoints, continue or step, inspect stack and
variables, optionally evaluate an expression, then stop the session:

```json
{"mode":"test","package":"./internal/foo","args":["-test.run","TestThing"]}
```

The first call is `debug_start`; it runs `dlv dap` and launches either a Go
package test session or a program launch session. v1 intentionally supports one
live debug session per yottacode session so later tool calls do not need a
session selector.

## Tools

| Tool | Purpose |
|---|---|
| `debug_start` | Start `dlv dap` in `launch` or `test` mode for a program/package |
| `debug_breakpoint` | Set a source breakpoint by `file` and 1-based `line` |
| `debug_continue` | Continue execution and wait up to 30 seconds for a stop event |
| `debug_step` | Run `next`, `stepIn`, or `stepOut` |
| `debug_stack` | Return stack frames for the current or supplied thread |
| `debug_vars` | Return variables for a frame/scope or DAP variables reference |
| `debug_eval` | Evaluate an expression in the selected frame/context |
| `debug_stop` | Disconnect and tear down Delve |

`debug_continue` and `debug_step` report `still running after 30s` when the
program does not stop within the default wait window. Tool output is capped so a
large variable tree or noisy adapter cannot flood the transcript.

## Approval policy

`debug_start` always requires approval because it executes user code or tests.
After that approval, breakpoint, continue, step, stack, vars, and stop operate
inside the already-approved debugger session without another prompt.

`debug_eval` also requires approval. Expression evaluation can be more powerful
than passive inspection depending on Delve/runtime behavior, so it keeps a
separate approval boundary.

## Scope boundaries

The implementation uses `dlv dap` only. It does not use Delve's headless RPC
mode and does not shell out to install or upgrade Delve. The low-level DAP
transport lives in `internal/dap`; Go/Delve session orchestration and the agent
tool surface live above that boundary.
