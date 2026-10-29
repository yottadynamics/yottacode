# ACP WSS production checklist

Before exposing the ACP WSS endpoint beyond localhost:

- use a dedicated service account and a host service manager;
- store the bearer token in a protected secret mechanism, not a shell history;
- use a certificate/key readable only by the service account;
- bind to localhost unless remote access is explicitly required;
- put a firewall or reverse proxy in front of remote deployments;
- rotate and revoke tokens regularly;
- grant `read`, `prompt`, and `approve` separately where possible;
- pin tokens to project roots;
- retain journal files on protected storage;
- monitor service restarts, authentication failures, replay gaps, and journal
  durability degradation;
- test controller takeover and pending-approval recovery before rollout.

The server remains a foreground process. `docs/yottacode-acp.service.example`
shows how to supervise it with systemd without adding a second daemon command
inside yottacode.
