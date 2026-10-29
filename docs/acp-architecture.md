# ACP remote-session architecture

This document describes the experimental ACP-over-WSS architecture and the
session ownership model. It is intentionally more detailed than the user-facing
CLI reference because it defines protocol boundaries and invariants.

## Scope

`yottacode acp` supports two transports:

- stdio for local editor embedding;
- WSS for authenticated remote ACP clients.

Both transports use the ACP SDK for protocol semantics. WSS adds a narrow
WebSocket framing adapter; it does not replace ACP JSON-RPC dispatch.

## Components

```mermaid
flowchart LR
    Client[ACP client\neditor / attach / remote UI]
    TLS[WSS + TLS\nBearer authentication]
    Adapter[WebSocket framing adapter\ntext frame <-> ACP stream]
    SDK[ACP SDK\nJSON-RPC dispatch + extensions]
    Server[internal/acp.Server\nshared session registry]
    Session[acpSession\nruntime + lifecycle + ownership]
    Runtime[agentruntime.Runtime\nagent turn + tools + persistence]
    Journal[(bounded event journal\nseq + replay gap)]
    Approvals[approval hub\ncontroller generation]
    Store[(Session persistence)]
    Tokens[(Token store\nhashed secrets + scopes)]

    Client --> TLS --> Adapter --> SDK --> Server --> Session --> Runtime
    Session --> Journal
    Session --> Approvals
    Runtime --> Store
    TLS --> Tokens
```

## Connection lifecycle

```mermaid
sequenceDiagram
    participant C as Remote ACP client
    participant H as WSS handler
    participant T as Token store
    participant A as ACP SDK
    participant S as ACP server

    C->>H: TLS + WebSocket upgrade\nAuthorization: Bearer ...
    H->>T: Authenticate token
    T-->>H: identity, scopes, project roots
    H-->>C: 101 Switching Protocols\n_yottacode.acp.v1
    H->>A: Create connection-bound Agent
    A->>S: initialize
    C->>A: session/new or session/load
    A->>S: authorize + register session
    S-->>C: ACP response and updates
```

Authentication happens before ACP initialization. An unauthenticated client
must never reach `initialize`, `session/load`, or an extension method.

## Ownership model

Each live session has:

- one optional controller;
- zero or more observers;
- one agent runtime;
- one lifecycle lock that serializes prompt, replacement, and close.

```mermaid
stateDiagram-v2
    [*] --> Idle
    Idle --> ControllerAttached: session/new or attach(controller)
    Idle --> Observing: attach(observer)
    ControllerAttached --> TurnRunning: prompt
    Observing --> Observing: replay / session updates
    TurnRunning --> ControllerAttached: turn complete
    ControllerAttached --> ControllerAttached: observer attach
    ControllerAttached --> ControllerAttached: takeover(controller)
    ControllerAttached --> Closed: close / shutdown
    Observing --> Closed: close / shutdown
    Closed --> [*]
```

Controller-only operations:

- prompt;
- cancel;
- close;
- session mode/configuration changes;
- permission decisions.

Observer operations:

- attach;
- replay;
- receive future updates.

Takeover must be explicit and requires `prompt` or `admin` scope. A normal
controller attach fails when another controller owns the session.

## Extension methods

ACP extension methods must begin with `_` according to the SDK and ACP
extensibility rules. Current names are:

| Method | Role | Purpose |
|---|---|---|
| `_yottacode/session/attach` | read/control | Attach as observer or controller |
| `_yottacode/session/replay` | read | Replay conversational history |
| `_yottacode/session/takeover` | control | Explicitly replace controller |

The extensions are yottacode-specific and are not a replacement for standard
ACP `session/new`, `session/load`, `session/prompt`, or `session/cancel`.

## Security invariants

- WSS requires TLS and bearer authentication.
- Raw bearer tokens are displayed once and never persisted.
- Token files are user-only (`0700` directory, `0600` file).
- Authentication uses constant-time comparison after salted hashing.
- Scopes are checked at ACP method dispatch, not only during WebSocket upgrade.
- Project-root restrictions are checked for session creation, loading, and attach.
- Session IDs do not grant access by themselves.
- A client disconnect does not implicitly approve a pending permission request.

## Current limitations

The implementation remains experimental. Implemented hardening now includes
bounded in-memory event cursors, replay-gap signaling, controller generations,
parked permission requests, WSS server timeouts, and attach role/takeover flags.
Durable event storage across daemon restarts, connection registries, rate
limiting, daemon start/stop/status commands, and full reconnect backoff remain
operational follow-up work.
