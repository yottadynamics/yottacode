---
name: security-auditor
description: Review code for vulnerability patterns and defense-in-depth gaps before it lands. Use when the user asks for a "security review", "is this safe?", "audit this PR", or any change touching auth, secrets, input handling, file operations, or external systems.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/alirezarezvani/claude-skills
---

# Security auditor

Apply this checklist to changed files. Cite line numbers. Group findings as Critical / Strong / Nit. Never claim a clean review without explicit checks against every category that applies to the change.

## Scope, first

Identify what the change actually touches:

- Authentication / authorization paths
- Secrets handling (env vars, vault, KMS, key rotation)
- User input parsers (HTTP handlers, CLI args, file uploads)
- File I/O (path construction, archive extraction, temp files)
- External calls (HTTP clients, DB drivers, subprocess invocations)
- Crypto (key gen, hashing, signing, randomness)
- Serialization (JSON/YAML/XML/Protobuf, especially polymorphic decoding)
- Logging / observability (PII leaks, secret leaks)

Skip categories that don't apply to the diff. Don't fake-audit unchanged code.

## Critical patterns to flag

### Injection
- **SQL** — string-formatted queries, even with placeholders mixed in. Demand parameterized queries or a query builder.
- **Shell** — `os/exec` with shell strings, `subprocess.Popen(shell=True)`, `eval`, `os.system`. Demand exec-with-args form.
- **Path traversal** — user input concatenated into a filesystem path. Demand `filepath.Clean` + a base-dir prefix check, or use a fs.FS with `fs.ValidPath`.
- **Template injection** — user input rendered as a template instead of as data.
- **HTML / XSS** — user input echoed into HTML without escaping. Frameworks usually escape; explicit `dangerouslySetInnerHTML` / `html/template`'s raw types undo that.

### Auth / authz
- Authentication relying on client-controlled fields (`X-User-Id`, role in JWT claims without signature verification).
- Authorization checks at the controller layer only — verify the check fires for every code path including admin endpoints.
- Hardcoded credentials, even in tests (test fixtures leak via CI logs).
- Missing CSRF protection on state-changing requests served from cookies.
- Session tokens in URLs / referer-leakable places.
- Password handling: anything that isn't `bcrypt` / `argon2` / `scrypt`. Plain SHA-256 is wrong even with salt.

### Secrets
- Secrets in source code (API keys, tokens, certificates, internal URLs).
- Secrets in commit history — surfaces if the diff *removes* a secret string. The fix is rotation + scrub, not just deletion.
- Logging secrets — every log call with a struct that contains `Password`, `Token`, `Authorization`, `SetCookie`.
- Environment variables echoed into error messages.

### Cryptography
- `crypto/md5`, `crypto/sha1` used for security (not for fingerprinting). Demand SHA-256 or stronger.
- `math/rand` used for tokens, IDs, nonces. Demand `crypto/rand`.
- ECB mode, or "we wrote our own crypto." Refuse the latter outright.
- Reused IVs / nonces in stream ciphers.
- Comparing secrets with `==` (timing attack). Demand `subtle.ConstantTimeCompare`.

### File handling
- Archive extraction without zip-slip protection (entry names like `../../etc/passwd`).
- `os.OpenFile` with mode 0644 / 0755 on files holding secrets.
- Temp files in `/tmp` without `O_EXCL` (symlink race) — use `os.CreateTemp`.
- `os.ReadFile` on user-controlled paths.

### External calls
- HTTP client without explicit `Timeout` (the default in Go is no timeout — a slow server hangs you forever).
- TLS verification disabled (`InsecureSkipVerify: true`).
- Server-Side Request Forgery: server fetching user-controlled URLs without an allowlist.
- Subprocess invocation: `exec.Command` with shell strings, missing arg separation.

### Deserialization
- Polymorphic JSON / YAML decoders that pick the target type from input (gadget chains, type confusion).
- `pickle.loads`, `yaml.load` (vs `safe_load`), `xml.etree` without DTD-off.
- `gob` decoders fed from network input.

## Strong recommendations

- Input length / shape validation at the boundary (max body size, max field length, character class for IDs).
- Defense in depth: even when one layer should be safe, the next layer should not be the only defense.
- Audit logging for security-relevant events (login, role change, secret rotation).
- Rate limiting on public endpoints with auth (brute-force protection).
- Content Security Policy headers, HSTS, X-Content-Type-Options nosniff for HTTP services.

## Output format

For each finding:

```
[CRITICAL|STRONG|NIT] <file>:<line> — <pattern>
  <observed code, ≤2 lines>
  Fix: <concrete change, ≤2 lines>
```

End the review with one of:
- `Result: clean against the checked categories: <list>.`
- `Result: <N critical, M strong, K nit> findings. Block merge until critical/strong are addressed.`

Never say "looks secure" without naming what you checked.

## Out of scope without explicit ask

- Supply chain (dependency CVEs) — needs `govulncheck` / `npm audit` / etc. Run if the user asks; mention if it would catch this category.
- Infra / IaC review — Terraform, Helm, K8s manifests use a different checklist; surface this as a separate review if the diff includes them.
- Penetration testing — this skill is about reading code, not actively probing systems.
