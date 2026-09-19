# Permission policy

Yottacode evaluates three optional permission policy files:

```text
/etc/yottacode/permissions.json
<repo>/.yottacode/permissions.json
<repo>/.yottacode/permissions.local.json
```

The system file is an administrator-managed, machine-wide baseline. It is read by yottacode but is never created or modified by approval flows. Install it as a root-owned, readable file such as `root:root` mode `0644` when ordinary users should be able to run yottacode under the policy.

The project `permissions.json` file is the committable team policy. The project `permissions.local.json` file is personal, gitignored policy for the current checkout. Automatic **always allow** and **always deny** decisions write only to the project-local file.

All loaded rules use the same precedence regardless of source:

```text
deny > ask > allow > session allow > normal approval
```

A local or project allow cannot override a deny or ask rule from the system policy. A missing policy file means no rules from that source. Malformed or unreadable existing files are reported as configuration errors rather than silently ignored.

The system policy is read-only in the picker; only project-shared and project-local rows can open an editor.
