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

The current implementation has per-session ownership and disconnect cleanup,
but no centralized registry or bounded outbound queues yet. That is the next
server-hardening phase. Until then, a slow observer can still participate in
the direct fan-out path and operational connection limits are host-level only.
