# ACP WSS server (experimental)

`yottacode acp` keeps the existing stdio ACP mode for editor integrations. A
remote ACP endpoint is available explicitly over WSS:

```bash
TOKEN=$(yottacode acp --token-create --token-name laptop \
  --token-scope read,prompt,approve | awk -F= '$1 == "token" {print $2}')
yottacode acp --listen 127.0.0.1:8443 \
  --tls-cert server.crt --tls-key server.key --token "$TOKEN"
```

The token command prints the opaque secret once. The daemon stores only a
salted hash under `~/.yottacode/acp-tokens.json` with mode `0600`.

See [ACP architecture](acp-architecture.md) for the transport, session
ownership, extension-method, and security design. See [ACP replay](acp-replay.md)
for event sequencing and reconnect cursors.

WSS clients must send:

```http
Authorization: Bearer <token>
Sec-WebSocket-Protocol: yottacode.acp.v1
```

Supported initial scopes are `read`, `prompt`, `approve`, and `admin`.
Remote ACP methods now enforce `read` for loading sessions and `prompt` for
creating, prompting, cancelling, closing, or changing session configuration.
The terminal recovery client is available as:

```bash
yottacode acp attach <session-id> --server wss://127.0.0.1:8443/acp --token "$TOKEN"
```

The attach client is intentionally minimal in this phase: it loads one session,
prints streamed assistant text, and submits newline-delimited terminal prompts.
Reconnect/replay cursors and controller takeover remain subsequent phases. Do not expose the endpoint to an
untrusted network yet; production deployment still requires connection limits,
rate limiting, project/session authorization, and daemon lifecycle controls.

For the legacy editor transport:

```bash
yottacode acp --stdio
```
