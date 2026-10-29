# ACP connection registry design

A connection registry is the server-side index of active WSS/stdio client
connections. It is separate from session persistence and from the ACP runtime.

```text
connection registry
  connection id
  authenticated token id
  scopes
  transport kind
  connected time
  last activity
  outbound queue
  attached session ids
  controller/observer roles
```

The registry enables operational controls that are not safe to implement by
scanning every session:

- maximum total connections;
- maximum connections per token;
- connection-level idle timeout;
- disconnect cleanup in one place;
- slow-client detection and eviction;
- per-connection audit events and metrics;
- mapping a WSS disconnect to all attached sessions.

The implementation now has a lightweight registry integrated at the WSS
boundary. Authenticated connections are admitted through a bounded registry,
receive queue-backed outbound delivery, and are removed on disconnect. Session
ownership remains authoritative for controller/observer roles; the registry is
only the deployment-level connection and backpressure control plane.
