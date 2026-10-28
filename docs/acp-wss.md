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

WSS clients must send:

```http
Authorization: Bearer <token>
Sec-WebSocket-Protocol: yottacode.acp.v1
```

Supported initial scopes are `read`, `prompt`, `approve`, and `admin`. Scope
enforcement beyond transport admission and the `acp attach` terminal client
are subsequent experimental phases. Do not expose the endpoint to an
untrusted network yet; production deployment still requires connection limits,
rate limiting, project/session authorization, and daemon lifecycle controls.

For the legacy editor transport:

```bash
yottacode acp --stdio
```
