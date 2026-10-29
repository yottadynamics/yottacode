# ACP-UI desktop smoke test

The supported test topology is two containers on one private network:

```text
acp-ui desktop container  ── WSS ──►  yottacode ACP server container
```

## Server container

Bind the server to the container interface:

```bash
yottacode acp \
  --listen 0.0.0.0:8443 \
  --tls-cert /etc/yottacode/server.crt \
  --tls-key /etc/yottacode/server.key \
  --token "$YOTTACODE_ACP_TOKEN"
```

Use a certificate whose SAN contains the service hostname used by acp-ui, for
example `acp-server`. Persist the server's `.yottacode` directory so sessions,
tokens, and replay journals survive container restarts.

## Desktop client container

Configure acp-ui with:

```text
wss://acp-server:8443/acp
```

The desktop client should send the bearer token either as an HTTP
`Authorization` header or using the WebSocket subprotocol form:

```text
Sec-WebSocket-Protocol: yottacode.acp.v1, yottacode.bearer.<token>
```

The client container must trust the CA that issued the server certificate.
Do not use `127.0.0.1`; inside the client container that refers to the client
container, not the server.

## Smoke-test sequence

1. Connect acp-ui and verify the WSS handshake.
2. Create a new ACP session in a server-side project directory.
3. Send a prompt and confirm streamed updates.
4. Disconnect and reconnect the desktop client.
5. Confirm the session transcript is restored without starting a second agent.
6. Open a second client as an observer.
7. Verify that only a controller with `approve` or `admin` scope can answer a
   permission request.

This is a controlled private-network test procedure, not a public-internet
deployment profile.
