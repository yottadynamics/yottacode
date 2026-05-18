---
name: remote-ops
description: SSH/scp/rsync playbook for connecting to remote hosts, running commands, copying files, and port-forwarding. Use when the user asks to operate on a remote server, deploy to a box, tail remote logs, or move files between machines.
license: MIT
metadata:
  author: yottacode
---

# Remote operations

Use this skill when the user asks to interact with a remote machine over SSH. The agent does NOT have a built-in ssh tool — every remote operation goes through `run_bash` (approval-gated). Plan the full command before invoking it.

## 1. Identify the target

Before running anything, confirm you know:

- **Host**: hostname, IP, or SSH alias (e.g. `prod-app-01`, `bastion.example.com`)
- **User**: login user (e.g. `deploy`, `ubuntu`, `root`); often baked into `~/.ssh/config` aliases
- **Auth**: key-based by default; never embed passwords in commands
- **Jump host / bastion**: if the target is reachable only via a bastion, plan a `-J <bastion>` flag rather than two hops

If the user said "the box" or "the server" without identifying it, ask which host before running anything.

## 2. Pick the right invocation

| Goal | Pattern |
|---|---|
| One-shot command | `ssh <user>@<host> -- <cmd>` (or `ssh <alias> <cmd>` with an `~/.ssh/config` alias) |
| Interactive session | Refuse — agent cannot drive an interactive TTY. Tell the user to run it themselves. |
| Copy a file to remote | `scp <local> <user>@<host>:<remote>` |
| Copy a file from remote | `scp <user>@<host>:<remote> <local>` |
| Sync a directory | `rsync -avz --delete <local>/ <user>@<host>:<remote>/` (trailing slashes matter) |
| Tail a log | `ssh <user>@<host> 'tail -F /path/to/log'` (wrap remote cmd in single quotes) |
| Port-forward (local) | `ssh -N -L <local-port>:<remote-host>:<remote-port> <user>@<jump>` — runs in foreground; backgrounding is the user's call |
| Run a script you just wrote | `scp ./script.sh <host>:/tmp/script.sh && ssh <host> 'bash /tmp/script.sh'` |

## 3. Quote remote commands correctly

A common foot-gun: shell expansion happens twice — once locally, once remotely. Use single quotes around the remote command to defer expansion, and escape any literal single quotes inside.

```bash
# Wrong — $HOME expands locally
ssh host echo $HOME

# Right — $HOME expands on the remote host
ssh host 'echo $HOME'
```

For multi-line commands, prefer a heredoc:

```bash
ssh host bash <<'EOF'
set -euo pipefail
cd /srv/app
git pull
systemctl restart app
EOF
```

`<<'EOF'` (quoted) prevents local expansion of `$var` in the heredoc body.

## 4. Safety checks before mutating commands

Before any command that mutates remote state (deploy, restart, write, delete), state what you're about to run and pause for the user to confirm. Even with run_bash's approval modal, the user sees `ssh prod 'rm -rf /var/log/old'` exactly once — make sure the destination and the verb are right.

Specifically refuse without explicit user confirmation:

- `rm -rf` on absolute paths over SSH
- `systemctl restart` / `reboot` / `shutdown` on production hosts
- `kill -9` against unidentified pids
- Anything writing to `/etc/` or `/usr/` on a remote box

## 5. Output handling

`ssh` mixes stdout and stderr; `run_bash` captures both. For long-running remote tails, the agent can't stream — use `ssh host 'tail -n 200 logfile'` for snapshots rather than `tail -F`, or ask the user to run the tail themselves.

For commands that produce more output than the agent should consume, pipe to `head -n 200` or write to a file and `scp` it back:

```bash
ssh host 'journalctl -u app --since "1 hour ago"' > /tmp/journal.txt
# inspect /tmp/journal.txt locally
```

## 6. Bastion / jump-host patterns

When the target sits behind a bastion:

```bash
ssh -J bastion.example.com app-01 'uptime'
```

Or set up a proxy in `~/.ssh/config` (one-time setup the user does themselves):

```
Host app-*
  ProxyJump bastion.example.com
  User deploy
```

After that, `ssh app-01 uptime` Just Works.

## 7. Common failure modes

- **`Permission denied (publickey)`** — wrong user, key not loaded, or key not on remote `~/.ssh/authorized_keys`. Check `ssh-add -L` locally.
- **`Host key verification failed`** — first-time connect, or the remote host's key changed. NEVER auto-accept by adding `-o StrictHostKeyChecking=no` without telling the user; that's a man-in-the-middle vulnerability. Surface the warning and let the user decide.
- **`Connection timed out`** — host unreachable, firewall, or wrong port. Try `nc -zv <host> 22` to confirm reachability.
- **Hanging commands** — the remote command is waiting on stdin. Pass `-n` to `ssh` to redirect stdin from `/dev/null`, or close the heredoc properly.

## 8. Never

- Embed passwords or private keys in commands. If the user provides one, refuse and explain why.
- Disable `StrictHostKeyChecking` without explicit user OK.
- Run with `sudo` over SSH without confirming the action and the host first.
- Assume a successful SSH means a successful operation — check the remote command's exit code separately.
